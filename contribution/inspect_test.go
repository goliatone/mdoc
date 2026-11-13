package contribution

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/mdoc/workingdraft"
)

func TestInspectionPreservesUncertaintyAndReceipts(t *testing.T) {
	ctx := context.Background()
	mode := "empty"
	var plan CreatePlan
	requests := 0
	s, e := New(Options{AccountBinding: "a", HTTPClient: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method != "GET" {
			t.Fatal("inspection wrote")
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			v := map[string]any{"files": []any{}}
			if mode == "incomplete" {
				v["incompleteSearch"] = true
			}
			if mode == "found" {
				v["files"] = []any{map[string]any{"id": "created", "mimeType": nativeDoc, "parents": []string{"folder"}, "appProperties": properties(plan)}}
			}
			return jsonResponse(v), nil
		}
		if strings.HasSuffix(r.URL.Path, "/replies") {
			return jsonResponse(map[string]any{"replies": []any{map[string]any{"id": "someone_else", "content": "Reply"}}}), nil
		}
		if strings.Contains(r.URL.Path, "/comments/") {
			return jsonResponse(map[string]any{"id": "comment", "resolved": true}), nil
		}
		if strings.Contains(r.URL.Host, "docs.") {
			return jsonResponse(nativeFixture("r1", []string{"abcd\n"}, nil)), nil
		}
		if strings.HasSuffix(r.URL.Path, "/files/created") {
			return jsonResponse(map[string]any{"id": "created", "mimeType": nativeDoc}), nil
		}
		return jsonResponse(map[string]any{"id": "doc", "mimeType": nativeDoc, "trashed": mode == "trashed"}), nil
	})}})
	if e != nil {
		t.Fatal(e)
	}
	plan = CreatePlan{Kind: CreateKind, Input: CreateInput{Meta: testMeta(), ParentID: "folder", Title: "Title"}, Artifact: Artifact{DOCX: []byte("artifact"), Digest: hash([]byte("artifact")), RendererVersion: "test"}}
	plan.Digest = createDigest(plan)
	result, e := s.InspectOutcome(ctx, InspectInput{Create: &plan})
	if e != nil || result.Evidence != AbsentAtRead || result.Outcome.Status != Unknown {
		t.Fatalf("%+v %v", result, e)
	}
	mode = "incomplete"
	result, e = s.InspectOutcome(ctx, InspectInput{Create: &plan})
	if e == nil || result.Evidence != Incomplete || result.Outcome.Status != Unknown {
		t.Fatal("incomplete discovery promoted")
	}
	mode = "found"
	result, e = s.InspectOutcome(ctx, InspectInput{Create: &plan})
	if e != nil || result.Outcome.Status != Committed || !result.Outcome.AcknowledgedAt.IsZero() {
		t.Fatalf("%+v %v", result, e)
	}
	receipt := result.Outcome
	mode = "empty"
	result, e = s.InspectOutcome(ctx, InspectInput{Create: &plan, Previous: receipt})
	if e != nil || result.Outcome.Status != Committed || result.Outcome.DocumentID != "created" {
		t.Fatal("historical receipt lost")
	}
	rp := ReplyPlan{Kind: ReplyKind, Input: ReplyInput{Meta: testMeta(), Source: workingdraft.SourceRef{Provider: workingdraft.ProviderGoogleDocs, DocumentID: "doc", TabID: "t.0"}, CommentID: "comment", Content: "Reply"}, Thread: ThreadState{Fingerprint: digest("fingerprint")}}
	rp.Digest = replyDigest(rp)
	result, e = s.InspectOutcome(ctx, InspectInput{Reply: &rp})
	if e != nil || result.Evidence != Ambiguous || result.Outcome.Status != Unknown || len(result.Candidates) != 1 {
		t.Fatalf("%+v %v", result, e)
	}
	before := requests
	bad := receipt
	bad.RequestDigest = "different"
	if _, e = s.InspectOutcome(ctx, InspectInput{Create: &plan, Previous: bad}); e == nil || requests != before {
		t.Fatal("mismatched receipt accepted")
	}
	mode = "trashed"
	prev := outcome(ReplyKind, rp.Input.Meta, rp.Digest, Attempt{"attempt", true})
	prev.Status = Committed
	prev.DocumentID = "doc"
	prev.TabID = "t.0"
	prev.CommentID = "comment"
	prev.ReplyID = "reply"
	result, e = s.InspectOutcome(ctx, InspectInput{Reply: &rp, Previous: prev})
	if e != nil || result.Outcome.Status != Committed || result.Evidence != AbsentAtRead {
		t.Fatal("trashed erased receipt")
	}
}

func TestInspectRetainedCreationIdentity(t *testing.T) {
	plan := CreatePlan{Kind: CreateKind, Input: CreateInput{Meta: testMeta(), ParentID: "folder", Title: "Title"}, Artifact: Artifact{DOCX: []byte("artifact"), Digest: hash([]byte("artifact")), RendererVersion: "test"}}
	plan.Digest = createDigest(plan)
	for _, tc := range []struct {
		name               string
		status             Status
		discovery          string
		getStatus          int
		trashed, drift     bool
		evidence           Evidence
		committed, wantErr bool
	}{
		{name: "committed-metadata-absent", status: Committed, discovery: "empty", drift: true, evidence: Observed, committed: true},
		{name: "committed-discovery-unavailable", status: Committed, discovery: "incomplete", evidence: Observed, committed: true},
		{name: "committed-trashed", status: Committed, trashed: true, evidence: Observed, committed: true},
		{name: "committed-access-denied", status: Committed, getStatus: 403, evidence: Inaccessible, committed: true, wantErr: true},
		{name: "committed-deleted-or-hidden", status: Committed, getStatus: 404, evidence: Inaccessible, committed: true, wantErr: true},
		{name: "unknown-metadata-absent", status: Unknown, discovery: "empty", drift: true, evidence: Observed},
		{name: "unknown-incomplete-discovery", status: Unknown, discovery: "incomplete", evidence: Incomplete, wantErr: true},
		{name: "unknown-exact-discovery", status: Unknown, discovery: "exact", evidence: Observed, committed: true},
		{name: "unknown-conflicting-discovery", status: Unknown, discovery: "other", evidence: Ambiguous, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gets, searches := 0, 0
			s, err := New(Options{AccountBinding: "a", HTTPClient: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("inspection mutated provider")
				}
				switch r.URL.Path {
				case "/drive/v3/files/created":
					gets++
					if tc.getStatus != 0 {
						resp := jsonResponse(map[string]any{"error": map[string]any{"code": tc.getStatus, "message": "unavailable"}})
						resp.StatusCode = tc.getStatus
						return resp, nil
					}
					props := properties(plan)
					parents := []string{"folder"}
					if tc.drift {
						props = nil
						parents = []string{"moved"}
					}
					return jsonResponse(map[string]any{"id": "created", "mimeType": nativeDoc, "parents": parents, "appProperties": props, "trashed": tc.trashed}), nil
				case "/drive/v3/files":
					searches++
					if gets != 1 {
						t.Fatal("discovery gated direct inspection")
					}
					result := map[string]any{"files": []any{}}
					if tc.discovery == "incomplete" {
						result["incompleteSearch"] = true
					}
					if tc.discovery == "exact" || tc.discovery == "other" {
						id := "created"
						if tc.discovery == "other" {
							id = "different"
						}
						result["files"] = []any{map[string]any{"id": id, "mimeType": nativeDoc, "parents": []string{"folder"}, "appProperties": properties(plan)}}
					}
					return jsonResponse(result), nil
				default:
					t.Fatalf("unexpected request: %s", r.URL)
					return nil, nil
				}
			})}})
			if err != nil {
				t.Fatal(err)
			}
			previous := outcome(CreateKind, plan.Input.Meta, plan.Digest, Attempt{ID: "attempt"})
			previous.Status = tc.status
			previous.DocumentID = "created"
			if tc.status == Committed {
				previous.AcknowledgedAt = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
			}
			result, err := s.InspectOutcome(context.Background(), InspectInput{Create: &plan, Previous: previous})
			if (err != nil) != tc.wantErr || result.Evidence != tc.evidence || gets != 1 {
				t.Fatalf("inspection %+v err=%v gets=%d", result, err, gets)
			}
			if tc.status == Committed && (!reflect.DeepEqual(result.Outcome, previous) || searches != 0) {
				t.Fatal("committed history replaced or discovery required")
			}
			if (result.Outcome.Status == Committed) != tc.committed {
				t.Fatal("incorrect recovery", result.Outcome)
			}
			if tc.status != Committed && !result.Outcome.AcknowledgedAt.IsZero() {
				t.Fatal("fabricated acknowledgment")
			}
			if tc.getStatus == 0 {
				observation := result.Creation
				if observation == nil || observation.DocumentID != "created" || observation.Trashed != tc.trashed || observation.MetadataMatches == tc.drift || observation.DestinationMatches == tc.drift || !observation.TopologyMatches {
					t.Fatalf("missing direct identity evidence: %+v", observation)
				}
			} else if result.Creation != nil {
				t.Fatal("fabricated inaccessible identity observation")
			}
		})
	}
}
