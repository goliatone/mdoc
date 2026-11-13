package cli

import (
	"bytes"
	"encoding/json"
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
