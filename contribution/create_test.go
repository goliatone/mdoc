package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func testMeta() OperationMeta {
	return OperationMeta{SchemaVersion: 1, Namespace: "test", OperationID: "0123456789abcdef0123456789abcdef", AccountBinding: "a"}
}
func jsonResponse(v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}
}
func TestCreateSingleAttempt(t *testing.T) {
	ctx := context.Background()
	writes := 0
	lost := false
	matches := []any{}
	s, e := New(Options{AccountBinding: "a", HTTPClient: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			writes++
			if r.URL.Query().Get("uploadType") != "multipart" {
				t.Fatal("resumable upload")
			}
			if lost {
				return nil, errors.New("lost response secret")
			}
			return jsonResponse(map[string]any{"id": "created"}), nil
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			return jsonResponse(map[string]any{"files": matches}), nil
		}
		return jsonResponse(map[string]any{"id": "folder", "mimeType": folderMime, "capabilities": map[string]bool{"canAddChildren": true}}), nil
	})}})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.PrepareCreate(ctx, CreateInput{Meta: testMeta(), ParentID: "folder", Title: "Example", Markdown: "# Hello\n\nA **bold** [link](https://example.com)."})
	if e != nil {
		t.Fatal(e)
	}
	if writes != 0 {
		t.Fatal("prepare wrote")
	}
	r := CreateRequest{Plan: p, ExpectedDigest: p.Digest, Attempt: Attempt{ID: "one", FirstTransmission: true}}
	o, e := s.Create(ctx, r)
	if e != nil || o.Status != Committed || writes != 1 {
		t.Fatalf("%+v %v", o, e)
	}
	matches = []any{map[string]any{"id": "created", "mimeType": nativeDoc, "parents": []string{"folder"}, "appProperties": properties(p)}}
	o, e = s.Create(ctx, r)
	if e != nil || o.Status != Committed || writes != 1 {
		t.Fatal("recovery sent")
	}
	matches = append(matches, matches[0])
	o, e = s.Create(ctx, r)
	if e == nil || o.Status != Conflict || writes != 1 {
		t.Fatal("duplicates accepted")
	}
	matches = nil
	lost = true
	o, e = s.Create(ctx, r)
	if e == nil || o.Status != Unknown || writes != 2 || strings.Contains(e.Error(), "secret") {
		t.Fatalf("%+v %v %d", o, e, writes)
	}
	r.Attempt.FirstTransmission = false
	o, e = s.Create(ctx, r)
	if e == nil || writes != 2 {
		t.Fatal("uncertain resend")
	}
}
func TestUnsupportedMarkdown(t *testing.T) {
	for _, v := range []string{"![x](https://example.com/x.png)", "[x](../secret)", "<b>raw</b>", "| a | b |\n|---|---|\n| c | d |", "```sh\nrun\n```"} {
		if _, e := (pandocRenderer{}).Render(context.Background(), v); e == nil {
			t.Fatalf("accepted %q", v)
		}
	}
}
