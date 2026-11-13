package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/contribution"
	"github.com/goliatone/mdoc/internal/contributionstate"
	"github.com/goliatone/mdoc/workingdraft"
)

type replyFixtureTransport struct{}

func (replyFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != "GET" {
		return nil, errors.New("unexpected mutation")
	}
	body := `{"id":"doc","mimeType":"application/vnd.google-apps.document","capabilities":{"canComment":true}}`
	switch {
	case strings.Contains(r.URL.Host, "docs."):
		body = `{"documentId":"doc","tabs":[{"tabProperties":{"tabId":"t.0"}}]}`
	case strings.HasSuffix(r.URL.Path, "/replies"):
		body = `{"replies":[]}`
	case strings.Contains(r.URL.Path, "/comments/"):
		body = `{"id":"comment"}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestContributionBrokenPipe(t *testing.T) {
	if os.Getenv("MDOC_TEST_BROKEN_PIPE") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"mdoc"}, os.Args[i+1:]...)
				main()
				os.Exit(0)
			}
		}
		t.Fatal("missing child arguments")
	}
	api, err := contribution.New(contribution.Options{AccountBinding: "account", HTTPClient: &http.Client{Transport: replyFixtureTransport{}}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := contribution.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := api.PrepareReply(context.Background(), contribution.ReplyInput{
		Meta:   contribution.OperationMeta{SchemaVersion: 1, Namespace: "pipe-test", OperationID: id, AccountBinding: "account"},
		Source: workingdraft.SourceRef{DocumentID: "doc"}, CommentID: "comment", Content: "reply",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	planPath, journal := filepath.Join(dir, "plan.json"), filepath.Join(dir, "journal")
	if err = os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := contributionstate.Open(journal)
	if err != nil {
		t.Fatal(err)
	}
	// A committed replay needs no account/network and must report commitment
	// through the recovery error when stdout cannot deliver the retained receipt.
	record := &contributionstate.Record{Version: 1, Plan: raw, State: "finished", Outcome: contribution.Outcome{
		Kind: contribution.ReplyKind, Meta: plan.Input.Meta, RequestDigest: plan.Digest, AttemptID: "attempt",
		Status: contribution.Committed, DocumentID: "doc", TabID: "t.0", CommentID: "comment", ReplyID: "reply",
	}}
	if err = store.Save(plan.Input.Meta.Namespace, id, record); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	defer writer.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestContributionBrokenPipe$", "--", "contribution", "reply", "--plan", planPath, "--journal", journal, "--expect-digest", plan.Digest, "--write")
	child.Env = append(os.Environ(), "MDOC_TEST_BROKEN_PIPE=1")
	child.Stdout = writer
	var stderr bytes.Buffer
	child.Stderr = &stderr
	err = child.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 8 {
		t.Fatalf("expected recovery exit 8, got %v, stderr=%s", err, &stderr)
	}
	var reported struct {
		Code string `json:"code"`
		Exit int    `json:"exit_code"`
	}
	if json.Unmarshal(stderr.Bytes(), &reported) != nil || reported.Code != string(contribution.ReconciliationRequired) || reported.Exit != 8 {
		t.Fatalf("missing structured recovery error: %s", &stderr)
	}
}
