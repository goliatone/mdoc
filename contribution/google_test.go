package contribution

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/mdoc/workingdraft"
	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTransportBounds(t *testing.T) {
	n := 0
	s, e := New(Options{AccountBinding: "a", HTTPClient: &http.Client{Timeout: time.Minute, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		n++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.invalid"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.file(context.Background(), "doc"); e == nil || n != 1 {
		t.Fatalf("redirect followed: %d %v", n, e)
	}
	if _, e = New(Options{AccountBinding: "a", HTTPClient: http.DefaultClient, TokenSource: oauth2.StaticTokenSource(&oauth2.Token{})}); e == nil {
		t.Fatal("ambiguous auth")
	}
	_, e = (boundTransport{transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponse+1)))}, nil
	})}).RoundTrip(&http.Request{})
	if e == nil {
		t.Fatal("unbounded response")
	}
}

func TestGuardedBatchRejectionClassification(t *testing.T) {
	o, e := mutationError(Outcome{Kind: UpdateKind}, &googleapi.Error{Code: 400})
	if e == nil || o.Status != Conflict || o.Code != Stale {
		t.Fatalf("%+v %v", o, e)
	}
	o, e = mutationError(Outcome{Kind: UpdateKind}, &googleapi.Error{Code: 503})
	if e == nil || o.Status != Unknown || e.(*Error).Status != Unknown {
		t.Fatalf("%+v %v", o, e)
	}
}

func TestWorkingDraftErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		in  workingdraft.ErrorCode
		out Code
	}{{workingdraft.UnsupportedContent, UnsupportedContent}, {workingdraft.RemoteUnstable, Stale}, {workingdraft.AccessDenied, AccessDenied}} {
		e := safe(&workingdraft.Error{Code: tc.in, Message: "provider detail"})
		if e.(*Error).Code != tc.out || strings.Contains(e.Error(), "provider detail") {
			t.Fatal(e)
		}
	}
}
