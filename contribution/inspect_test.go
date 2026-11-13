package contribution

import (
	"context"
	"net/http"
	"strings"
	"testing"

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
