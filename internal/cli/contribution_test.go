package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/contribution"
	"github.com/goliatone/mdoc/internal/app"
	"github.com/goliatone/mdoc/internal/contributionstate"
	"github.com/goliatone/mdoc/workingdraft"
)

type contributionRoundTrip func(*http.Request) (*http.Response, error)

func (f contributionRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestContributionRegisteredCLIRecoverySmoke(t *testing.T) {
	rev := "r1"
	body := "abcd"
	createWrites, updateWrites, replyWrites := 0, 0, 0
	var created map[string]any
	lost := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		respond := func(v any) { json.NewEncoder(w).Encode(v) }
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/upload/"):
			createWrites++
			_, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if e != nil {
				t.Error(e)
				return
			}
			part, e := multipart.NewReader(r.Body, params["boundary"]).NextPart()
			if e != nil {
				t.Error(e)
				return
			}
			json.NewDecoder(part).Decode(&created)
			created["id"] = "doc"
			respond(map[string]any{"id": "doc"})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, ":batchUpdate"):
			updateWrites++
			var batch struct {
				Requests     []map[string]any  `json:"requests"`
				WriteControl map[string]string `json:"writeControl"`
			}
			json.NewDecoder(r.Body).Decode(&batch)
			if batch.WriteControl["requiredRevisionId"] != rev || len(batch.Requests) != 2 {
				t.Error("wrong batch precondition")
			}
			body = "aXd"
			rev = "r3"
			respond(map[string]any{"documentId": "doc"})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/comments/comment/replies"):
			replyWrites++
			if lost {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			respond(map[string]any{"id": "reply"})
		case r.Method != "GET":
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		case strings.HasSuffix(r.URL.Path, "/files"):
			files := []any{}
			if created != nil {
				files = append(files, created)
			}
			respond(map[string]any{"files": files})
		case strings.HasSuffix(r.URL.Path, "/files/folder"):
			respond(map[string]any{"id": "folder", "mimeType": "application/vnd.google-apps.folder", "capabilities": map[string]bool{"canAddChildren": true}})
		case strings.Contains(r.URL.Path, "/documents/doc"):
			run := map[string]any{"startIndex": 1, "endIndex": len(body) + 2, "textRun": map[string]any{"content": body + "\n"}}
			paragraph := map[string]any{"startIndex": 1, "endIndex": len(body) + 2, "paragraph": map[string]any{"elements": []any{run}}}
			tab := map[string]any{"tabProperties": map[string]any{"tabId": "t.0"}, "documentTab": map[string]any{"body": map[string]any{"content": []any{paragraph}}}}
			respond(map[string]any{"documentId": "doc", "title": "Draft", "revisionId": rev, "tabs": []any{tab}})
		case strings.HasSuffix(r.URL.Path, "/replies/reply"):
			respond(map[string]any{"id": "reply", "content": "Reply"})
		case strings.HasSuffix(r.URL.Path, "/replies"):
			respond(map[string]any{"replies": []any{}})
		case strings.HasSuffix(r.URL.Path, "/comments/comment"):
			respond(map[string]any{"id": "comment", "content": "Question", "modifiedTime": "t1"})
		case strings.HasSuffix(r.URL.Path, "/files/doc"):
			respond(map[string]any{"id": "doc", "name": "Draft", "version": "1", "mimeType": "application/vnd.google-apps.document", "capabilities": map[string]bool{"canEdit": true, "canComment": true}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	local, _ := url.Parse(server.URL)
	client := &http.Client{Transport: contributionRoundTrip(func(r *http.Request) (*http.Response, error) {
		cloned := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = local.Scheme
		u.Host = local.Host
		cloned.URL = &u
		return http.DefaultTransport.RoundTrip(cloned)
	})}
	api, e := contribution.New(contribution.Options{AccountBinding: "account", HTTPClient: client})
	if e != nil {
		t.Fatal(e)
	}
	service := &app.Contributions{API: api, Binding: "account"}
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal")
	run := func(args ...string) ([]byte, error) {
		var out, errs bytes.Buffer
		e := Execute(append([]string{"contribution"}, args...), &out, &errs, &fakeAuth{}, service)
		return out.Bytes(), e
	}
	prepare := func(command string, input any) (string, string, []byte) {
		b, _ := json.Marshal(input)
		path := filepath.Join(dir, "input.json")
		os.WriteFile(path, b, 0600)
		out, e := run(command, "--input", path, "--journal", journal)
		if e != nil {
			t.Fatalf("%s: %v %s", command, e, out)
		}
		var result struct {
			Digest string `json:"digest"`
		}
		json.Unmarshal(out, &result)
		planPath := filepath.Join(dir, result.Digest+".json")
		os.WriteFile(planPath, out, 0600)
		return planPath, result.Digest, out
	}
	execute := func(command, path, digest string) (contribution.Outcome, error) {
		out, e := run(command, "--plan", path, "--expect-digest", digest, "--journal", journal, "--write")
		var result contribution.Outcome
		if json.Unmarshal(out, &result) != nil {
			t.Fatalf("missing structured outcome %s: %v", out, e)
		}
		return result, e
	}
	meta := func() contribution.OperationMeta {
		id, _ := contribution.NewOperationID()
		return contribution.OperationMeta{SchemaVersion: 1, Namespace: "cli-smoke", OperationID: id}
	}
	path, dg, _ := prepare("prepare-create", contribution.CreateInput{Meta: meta(), ParentID: "folder", Title: "Draft", Markdown: "A **document**."})
	o, e := execute("create", path, dg)
	if e != nil || o.Status != contribution.Committed {
		t.Fatalf("%+v %v", o, e)
	}
	service = &app.Contributions{API: api, Binding: "account"}
	o, e = execute("create", path, dg)
	if e != nil || createWrites != 1 {
		t.Fatal("create replay sent")
	}
	// The retained identity remains inspectable after operation metadata is
	// removed; the file GET fixture deliberately has no contribution properties.
	created = nil
	inspected, e := run("inspect", "--plan", path, "--journal", journal)
	var creationInspection contribution.Inspection
	if json.Unmarshal(inspected, &creationInspection) != nil || e != nil || creationInspection.Outcome.Status != contribution.Committed || creationInspection.Evidence != contribution.Observed || creationInspection.Creation == nil || creationInspection.Creation.MetadataMatches || createWrites != 1 {
		t.Fatalf("retained create inspection lost identity/history: %s %v", inspected, e)
	}
	update := contribution.UpdateInput{Meta: meta(), Source: workingdraft.SourceRef{DocumentID: "doc"}, ExpectedRevision: rev, Selection: contribution.TextSelection{ParagraphStart: 1, Start: 1, End: 3, ExpectedText: "bc", ParagraphDigest: contribution.ParagraphDigest(body)}, Replacement: "X"}
	path, dg, _ = prepare("prepare-update", update)
	rev = "r2"
	o, e = execute("apply-update", path, dg)
	if e == nil || o.Status != contribution.Conflict || updateWrites != 0 {
		t.Fatal("stale update wrote")
	}
	update.Meta = meta()
	update.ExpectedRevision = rev
	path, dg, _ = prepare("prepare-update", update)
	o, e = execute("apply-update", path, dg)
	if e != nil || o.Status != contribution.Committed || updateWrites != 1 {
		t.Fatalf("fresh update %+v %v", o, e)
	}
	if out, e := run("inspect", "--plan", path, "--journal", journal); e != nil || !bytes.Contains(out, []byte(`"postcondition": true`)) {
		t.Fatalf("update inspect %s %v", out, e)
	}
	reply := contribution.ReplyInput{Meta: meta(), Source: workingdraft.SourceRef{DocumentID: "doc"}, CommentID: "comment", Content: "Reply"}
	path, dg, _ = prepare("prepare-reply", reply)
	repeatedPath, repeatedDigest, _ := prepare("prepare-reply", reply)
	if repeatedPath != path || repeatedDigest != dg {
		t.Fatal("same input did not reuse retained plan")
	}
	o, e = execute("reply", path, dg)
	if e != nil || o.Status != contribution.Committed || replyWrites != 1 {
		t.Fatal("reply failed", e)
	}
	if _, e = run("inspect", "--plan", path, "--journal", journal); e != nil {
		t.Fatal(e)
	}
	reply.Meta = meta()
	path, dg, raw := prepare("prepare-reply", reply)
	lost = true
	o, e = execute("reply", path, dg)
	if e == nil || o.Status != contribution.Unknown || replyWrites != 2 {
		t.Fatalf("lost response %+v %v", o, e)
	}
	service = &app.Contributions{API: api, Binding: "account"}
	if _, e = execute("reply", path, dg); e == nil || replyWrites != 2 {
		t.Fatal("restart resent unknown")
	}
	if out, e := run("inspect", "--plan", path, "--journal", journal); e != nil || !bytes.Contains(out, []byte(`"status": "unknown"`)) {
		t.Fatal("inspection erased uncertainty", e, string(out))
	}
	var plan contribution.ReplyPlan
	json.Unmarshal(raw, &plan)
	store, e := contributionstate.Open(journal)
	if e != nil {
		t.Fatal(e)
	}
	record, e := store.Load(plan.Input.Meta.Namespace, plan.Input.Meta.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	record.State = "sending"
	record.Outcome.Status = contribution.Unknown
	if e = store.Save(plan.Input.Meta.Namespace, plan.Input.Meta.OperationID, record); e != nil {
		t.Fatal(e)
	}
	store.Close()
	if _, e = execute("reply", path, dg); e == nil || replyWrites != 2 {
		t.Fatal("crashed sending claim resent")
	}
	store, e = contributionstate.Open(journal)
	if e != nil {
		t.Fatal(e)
	}
	record, e = store.Load(plan.Input.Meta.Namespace, plan.Input.Meta.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	record.Outcome.Meta.Namespace = "foreign"
	if e = store.Save(plan.Input.Meta.Namespace, plan.Input.Meta.OperationID, record); e != nil {
		t.Fatal(e)
	}
	store.Close()
	if _, e = execute("reply", path, dg); e == nil || replyWrites != 2 {
		t.Fatal("inconsistent retained receipt accepted")
	}
	t.Logf("real SDK/registered CLI smoke: create=%d update=%d replies=%d (one lost response), restart and sending recovery suppressed resend", createWrites, updateWrites, replyWrites)
}

func TestContributionReceiptPersistenceRecovery(t *testing.T) {
	for _, lost := range []bool{true, false} {
		name := "acknowledged"
		if lost {
			name = "response-lost"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			journal := filepath.Join(dir, "journal")
			// Make the path unavailable after provider dispatch while preserving the
			// opened journal and its durable sending claim for subsequent recovery.
			unavailable := false
			disrupt := func() {
				t.Helper()
				if unavailable {
					return
				}
				if err := os.Rename(journal, journal+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journal, []byte("unavailable"), 0600); err != nil {
					t.Fatal(err)
				}
				unavailable = true
			}
			restore := func() {
				t.Helper()
				if !unavailable {
					return
				}
				if err := os.Remove(journal); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(journal+".retained", journal); err != nil {
					t.Fatal(err)
				}
				unavailable = false
			}
			defer restore()
			sends := 0
			inspecting := false
			client := &http.Client{Transport: contributionRoundTrip(func(r *http.Request) (*http.Response, error) {
				b := `{}`
				switch {
				case r.Method == "POST":
					sends++
					disrupt()
					if lost {
						return nil, errors.New("response lost")
					}
					b = `{"id":"reply"}`
				case strings.Contains(r.URL.Host, "docs."):
					b = `{"documentId":"doc","tabs":[{"tabProperties":{"tabId":"t.0"}}]}`
				case strings.HasSuffix(r.URL.Path, "/replies"):
					b = `{"replies":[]}`
				case strings.Contains(r.URL.Path, "/comments/"):
					b = `{"id":"comment"}`
				default:
					b = `{"id":"doc","mimeType":"application/vnd.google-apps.document","capabilities":{"canComment":true}}`
				}
				if inspecting {
					disrupt()
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(b))}, nil
			})}
			api, err := contribution.New(contribution.Options{AccountBinding: "account", HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			service := &app.Contributions{API: api, Binding: "account"}
			run := func(args ...string) ([]byte, error) {
				t.Helper()
				var out, errs bytes.Buffer
				err := Execute(append([]string{"contribution"}, args...), &out, &errs, &fakeAuth{}, service)
				return out.Bytes(), err
			}
			input := filepath.Join(dir, "input.json")
			if err = os.WriteFile(input, []byte(`{"meta":{"namespace":"receipt-regression"},"source":{"document_id":"doc"},"comment_id":"comment","content":"reply"}`), 0600); err != nil {
				t.Fatal(err)
			}
			raw, err := run("prepare-reply", "--input", input, "--journal", journal)
			if err != nil {
				t.Fatal(err)
			}
			var plan contribution.ReplyPlan
			if err = json.Unmarshal(raw, &plan); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "plan.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			checkRecovery := func(err error, status contribution.Status) {
				t.Helper()
				var typed *contribution.Error
				if !errors.As(err, &typed) || typed.Code != contribution.ReconciliationRequired || typed.Status != status || ExitCode(err) != 8 {
					t.Fatalf("lost recovery semantics: %v, exit=%d", err, ExitCode(err))
				}
				var encoded bytes.Buffer
				WriteError(&encoded, err, true)
				var output ErrorOutput
				if e := json.Unmarshal(encoded.Bytes(), &output); e != nil || output.Code != string(contribution.ReconciliationRequired) || output.ExitCode != 8 {
					t.Fatalf("structured error: %s", encoded.String())
				}
			}
			raw, err = run("reply", "--plan", path, "--expect-digest", plan.Digest, "--journal", journal, "--write")
			var outcome contribution.Outcome
			if e := json.Unmarshal(raw, &outcome); e != nil {
				t.Fatal(e)
			}
			want := contribution.Committed
			if lost {
				want = contribution.Unknown
			}
			if outcome.Status != want || sends != 1 || (!lost && outcome.ReplyID != "reply") {
				t.Fatalf("provider outcome erased: %+v sends=%d", outcome, sends)
			}
			checkRecovery(err, want)
			restore()
			checkSending := func() {
				t.Helper()
				store, e := contributionstate.Open(journal)
				if e != nil {
					t.Fatal(e)
				}
				defer store.Close()
				record, e := store.Load(plan.Input.Meta.Namespace, plan.Input.Meta.OperationID)
				if e != nil || record == nil || record.State != "sending" {
					t.Fatal("claim lost", record, e)
				}
			}
			checkSending()
			service = &app.Contributions{API: api, Binding: "account"}
			raw, err = run("reply", "--plan", path, "--expect-digest", plan.Digest, "--journal", journal, "--write")
			if err == nil || ExitCode(err) != 8 || sends != 1 || !bytes.Contains(raw, []byte(`"status": "unknown"`)) {
				t.Fatal("restart resent or erased uncertainty", err, string(raw))
			}
			inspecting = true
			raw, err = run("inspect", "--plan", path, "--journal", journal)
			var inspection contribution.Inspection
			if e := json.Unmarshal(raw, &inspection); e != nil {
				t.Fatal(e)
			}
			checkRecovery(err, contribution.Unknown)
			if inspection.Outcome.Status != contribution.Unknown || sends != 1 {
				t.Fatal("inspection erased uncertainty or sent")
			}
			restore()
			checkSending()
			inspecting = false
			if _, err = run("inspect", "--plan", path, "--journal", journal); err != nil {
				t.Fatal("inspection did not recover after storage repair", err)
			}
			if sends != 1 {
				t.Fatal("recovery resent")
			}
		})
	}
}

// Accept a prefix, then fail, as with a full output file or a closed consumer.
type contributionFailedOutput struct{}

func (contributionFailedOutput) Write(p []byte) (int, error) {
	return min(16, len(p)), io.ErrClosedPipe
}

func TestContributionOutputFailureRecovery(t *testing.T) {
	for _, tc := range []struct {
		name         string
		lost, noSave bool
	}{
		{"unknown", true, false},
		{"committed", false, false},
		{"unknown-receipt-failure", true, true},
		{"committed-receipt-failure", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			journal := filepath.Join(dir, "journal")
			unavailable := false
			disrupt := func() {
				t.Helper()
				if !tc.noSave || unavailable {
					return
				}
				if err := os.Rename(journal, journal+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journal, nil, 0600); err != nil {
					t.Fatal(err)
				}
				unavailable = true
			}
			restore := func() {
				t.Helper()
				if unavailable {
					if err := os.Remove(journal); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(journal+".retained", journal); err != nil {
						t.Fatal(err)
					}
					unavailable = false
				}
			}
			defer restore()
			sends := 0
			inspecting := false
			api, err := contribution.New(contribution.Options{AccountBinding: "account", HTTPClient: &http.Client{Transport: contributionRoundTrip(func(r *http.Request) (*http.Response, error) {
				b := `{}`
				switch {
				case r.Method == "POST":
					sends++
					disrupt()
					if tc.lost {
						return nil, errors.New("response lost")
					}
					b = `{"id":"reply"}`
				case strings.Contains(r.URL.Host, "docs."):
					b = `{"documentId":"doc","tabs":[{"tabProperties":{"tabId":"t.0"}}]}`
				case strings.HasSuffix(r.URL.Path, "/replies"):
					b = `{"replies":[]}`
				case strings.HasSuffix(r.URL.Path, "/replies/reply"):
					b = `{"id":"reply","content":"reply"}`
				case strings.Contains(r.URL.Path, "/comments/"):
					b = `{"id":"comment"}`
				default:
					b = `{"id":"doc","mimeType":"application/vnd.google-apps.document","capabilities":{"canComment":true}}`
				}
				if inspecting {
					disrupt()
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(b))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			service := &app.Contributions{API: api, Binding: "account"}
			run := func(out io.Writer, args ...string) error {
				return Execute(append([]string{"contribution"}, args...), out, io.Discard, &fakeAuth{}, service)
			}
			input := filepath.Join(dir, "input.json")
			if err = os.WriteFile(input, []byte(`{"meta":{"namespace":"output-regression"},"source":{"document_id":"doc"},"comment_id":"comment","content":"reply"}`), 0600); err != nil {
				t.Fatal(err)
			}
			var raw bytes.Buffer
			if err = run(&raw, "prepare-reply", "--input", input, "--journal", journal); err != nil {
				t.Fatal(err)
			}
			plan, err := contribution.DecodeReplyPlan(raw.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "plan.json")
			if err = os.WriteFile(path, raw.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"reply", "--plan", path, "--expect-digest", plan.Digest, "--journal", journal, "--write"}
			check := func(err error, status contribution.Status) {
				t.Helper()
				var typed *contribution.Error
				if !errors.Is(err, io.ErrClosedPipe) || !errors.As(err, &typed) || typed.Code != contribution.ReconciliationRequired || typed.Status != status || ExitCode(err) != 8 {
					t.Fatalf("lost outcome or output error: %v (exit %d)", err, ExitCode(err))
				}
				var encoded bytes.Buffer
				WriteError(&encoded, err, true)
				var output ErrorOutput
				if json.Unmarshal(encoded.Bytes(), &output) != nil || output.Class != "contribution" || output.ExitCode != 8 || output.Code != string(contribution.ReconciliationRequired) || !strings.Contains(output.Message, "without resending") {
					t.Fatalf("missing recovery guidance: %s", &encoded)
				}
			}
			want := contribution.Committed
			if tc.lost {
				want = contribution.Unknown
			}
			err = run(contributionFailedOutput{}, args...)
			check(err, want)
			if tc.noSave && !strings.Contains(err.Error(), "receipt could not be durably saved") {
				t.Fatal("receipt error discarded", err)
			}
			if tc.lost && !strings.Contains(err.Error(), "Google request failed") {
				t.Fatal("provider error discarded", err)
			}
			restore()
			store, err := contributionstate.Open(journal)
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Load(plan.Input.Meta.Namespace, plan.Input.Meta.OperationID)
			store.Close()
			if err != nil || record == nil {
				t.Fatal("missing claim", err)
			}
			if tc.noSave {
				want = contribution.Unknown
				if record.State != "sending" {
					t.Fatal("sending claim lost", record.State)
				}
			}
			if record.Outcome.Status != want {
				t.Fatal("output failure changed receipt", record.Outcome.Status)
			}
			service = &app.Contributions{API: api, Binding: "account"}
			// Replay must preserve the same receipt and never send again.
			check(run(contributionFailedOutput{}, args...), want)
			inspecting = true
			check(run(contributionFailedOutput{}, "inspect", "--plan", path, "--journal", journal), want)
			restore()
			inspecting = false
			if err = run(io.Discard, "inspect", "--plan", path, "--journal", journal); err != nil {
				t.Fatal("recovery after restoring output/storage", err)
			}
			if sends != 1 {
				t.Fatal("recovery resent", sends)
			}
		})
	}
}

func TestContributionOutputPreservesPreflightErrors(t *testing.T) {
	for _, code := range []contribution.Code{contribution.AccessDenied, contribution.Stale, contribution.ReconciliationRequired} {
		original := &contribution.Error{Code: code, Status: contribution.NotSent, Message: "preflight or persistence failure"}
		outcome := contribution.Outcome{Status: contribution.NotSent}
		err := writeContributionResult(contributionFailedOutput{}, outcome, outcome, original)
		if !errors.Is(err, original) || !errors.Is(err, io.ErrClosedPipe) || ExitCode(err) != ExitCode(original) {
			t.Fatalf("classification changed: %v", err)
		}
		if err = writeContributionResult(io.Discard, outcome, outcome, original); err != original {
			t.Fatal("successful output changed error", err)
		}
	}
}
