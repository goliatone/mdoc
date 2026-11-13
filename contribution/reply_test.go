package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/workingdraft"
)

func TestReplyIndependentAndSingleSend(t *testing.T) {
	writes := 0
	modified := "t1"
	resolved := false
	deleted := false
	lost := false
	partial := false
	missing := false
	s, e := New(Options{AccountBinding: "a", HTTPClient: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			writes++
			if !strings.HasSuffix(r.URL.Path, "/files/doc/comments/comment/replies") {
				t.Fatal(r.URL)
			}
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			json.Unmarshal(b, &body)
			if len(body) != 1 || body["content"] != "Reply" {
				t.Fatal(string(b))
			}
			if lost {
				return nil, errors.New("lost")
			}
			if missing {
				return jsonResponse(map[string]any{}), nil
			}
			return jsonResponse(map[string]any{"id": "reply"}), nil
		}
		if strings.Contains(r.URL.Host, "docs.") {
			return jsonResponse(map[string]any{"documentId": "doc", "tabs": []any{map[string]any{"tabProperties": map[string]any{"tabId": "t.0"}, "documentTab": map[string]any{"body": map[string]any{"content": []any{map[string]any{"table": map[string]any{}}}}}}}}), nil
		}
		if strings.HasSuffix(r.URL.Path, "/replies") {
			v := map[string]any{"replies": []any{}}
			if partial {
				v["nextPageToken"] = "repeated"
			}
			return jsonResponse(v), nil
		}
		if strings.Contains(r.URL.Path, "/comments/") {
			return jsonResponse(map[string]any{"id": "comment", "content": "Question", "modifiedTime": modified, "resolved": resolved, "deleted": deleted}), nil
		}
		return jsonResponse(map[string]any{"id": "doc", "mimeType": nativeDoc, "capabilities": map[string]bool{"canComment": true}}), nil
	})}})
	if e != nil {
		t.Fatal(e)
	}
	in := ReplyInput{Meta: testMeta(), Source: workingdraft.SourceRef{DocumentID: "doc"}, CommentID: "comment", Content: "Reply"}
	p, e := s.PrepareReply(context.Background(), in)
	if e != nil || writes != 0 {
		t.Fatal(e)
	}
	r := ReplyRequest{p, p.Digest, Attempt{"reply", true}}
	modified = "t2"
	if o, e := s.Reply(context.Background(), r); e == nil || o.Status != Conflict || writes != 0 {
		t.Fatal("stale reply")
	}
	modified = "t1"
	resolved = true
	if _, e := s.Reply(context.Background(), r); e == nil || writes != 0 {
		t.Fatal("resolved reply")
	}
	resolved = false
	deleted = true
	if _, e := s.Reply(context.Background(), r); e == nil {
		t.Fatal("deleted reply")
	}
	deleted = false
	partial = true
	if _, e := s.PrepareReply(context.Background(), in); e == nil {
		t.Fatal("partial thread")
	}
	partial = false
	o, e := s.Reply(context.Background(), r)
	if e != nil || o.Status != Committed || o.ReplyID != "reply" || writes != 1 {
		t.Fatalf("%+v %v", o, e)
	}
	lost = true
	o, e = s.Reply(context.Background(), r)
	if e == nil || o.Status != Unknown || writes != 2 {
		t.Fatal("lost response")
	}
	r.Attempt.FirstTransmission = false
	if _, e := s.Reply(context.Background(), r); e == nil || writes != 2 {
		t.Fatal("resend")
	}
	r.Attempt.FirstTransmission = true
	lost = false
	missing = true
	o, e = s.Reply(context.Background(), r)
	if e == nil || o.Status != Unknown || writes != 3 {
		t.Fatal("missing reply ID")
	}
}
