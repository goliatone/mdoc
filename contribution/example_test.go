package contribution_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/contribution"
	"github.com/goliatone/mdoc/workingdraft"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This external-package caller imports no mdoc internal package. Durable claims
// belong to the embedding application; the CLI's separate journal exercises
// persistence/restart behavior in its production-path smoke.
func TestExternalConsumerReply(t *testing.T) {
	writes := 0
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		switch {
		case r.Method == "POST":
			writes++
			body = `{"id":"reply"}`
		case strings.Contains(r.URL.Host, "docs."):
			body = `{"documentId":"doc","tabs":[{"tabProperties":{"tabId":"t.0"}}]}`
		case strings.HasSuffix(r.URL.Path, "/replies"):
			body = `{"replies":[]}`
		case strings.Contains(r.URL.Path, "/comments/"):
			body = `{"id":"comment","modifiedTime":"t1"}`
		default:
			body = `{"id":"doc","mimeType":"application/vnd.google-apps.document","capabilities":{"canComment":true}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	service, e := contribution.New(contribution.Options{AccountBinding: "connection", HTTPClient: client})
	if e != nil {
		t.Fatal(e)
	}
	id, _ := contribution.NewOperationID()
	plan, e := service.PrepareReply(context.Background(), contribution.ReplyInput{Meta: contribution.OperationMeta{SchemaVersion: 1, Namespace: "external", OperationID: id, AccountBinding: "connection"}, Source: workingdraft.SourceRef{DocumentID: "doc"}, CommentID: "comment", Content: "Reviewed."})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(plan)
	plan, e = contribution.DecodeReplyPlan(b)
	if e != nil {
		t.Fatal(e)
	}
	result, e := service.Reply(context.Background(), contribution.ReplyRequest{Plan: plan, ExpectedDigest: plan.Digest, Attempt: contribution.Attempt{ID: "durably-claimed-attempt", FirstTransmission: true}})
	if e != nil || result.Status != contribution.Committed || writes != 1 {
		t.Fatalf("%+v %v", result, e)
	}
}
func ExampleParagraphDigest() {
	fmt.Println(len(contribution.ParagraphDigest("abcd")))
	// Output: 64
}
