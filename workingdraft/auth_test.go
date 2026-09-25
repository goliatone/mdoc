package workingdraft_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	wd "github.com/goliatone/mdoc/workingdraft"
	"golang.org/x/oauth2"
)

type credentials struct {
	mu      sync.Mutex
	records map[string]wd.CredentialRecord
}

func (c *credentials) Load(_ context.Context, key string) (wd.CredentialRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record := c.records[key]
	if record.Token != nil {
		copy := *record.Token
		record.Token = &copy
	}
	return record, nil
}
func (c *credentials) CompareAndSwap(_ context.Context, key, expected string, next wd.CredentialRecord) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.records[key].Revision != expected {
		return false, nil
	}
	if c.records == nil {
		c.records = map[string]wd.CredentialRecord{}
	}
	if next.Token != nil {
		copy := *next.Token
		next.Token = &copy
	}
	c.records[key] = next
	return true, nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOAuthBoundAccountAndOneUse(t *testing.T) {
	ctx := context.Background()
	store := &credentials{}
	calls := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if err := r.ParseForm(); err != nil || r.Form.Get("code_verifier") == "" {
			t.Error("missing PKCE verifier")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"access_token":"private-access","refresh_token":"private-refresh","token_type":"Bearer","expires_in":3600}`))}, nil
	})}
	service, err := wd.NewAuth(wd.AuthOptions{ClientID: "client", RedirectURL: "http://127.0.0.1/callback", Store: store, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	start, err := service.Start(ctx, "actor/project/account")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(start.AuthorizationURL, "code_challenge=") || !strings.Contains(start.AuthorizationURL, "drive.file") {
		t.Fatal("missing PKCE or scope")
	}
	if _, err = service.Finish(ctx, wd.AuthFinish{AccountRef: "other", State: start.State, Code: "code"}); err == nil {
		t.Fatal("accepted different host scope")
	}
	if _, err = service.Finish(ctx, wd.AuthFinish{AccountRef: "actor/project/account", State: start.State, Code: "code"}); err == nil {
		t.Fatal("replayed consumed state")
	}
	if calls != 0 {
		t.Fatal("exchanged before validating transaction")
	}
	start, _ = service.Start(ctx, "actor/project/account")
	status, err := service.Finish(ctx, wd.AuthFinish{AccountRef: "actor/project/account", State: start.State, Code: "code"})
	if err != nil || !status.Connected || store.records["actor/project/account"].Token == nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err = service.Finish(ctx, wd.AuthFinish{AccountRef: "actor/project/account", State: start.State, Code: "code"}); err == nil {
		t.Fatal("replayed successful exchange")
	}
}
func TestTransportErrorsNeverExposeProviderSecrets(t *testing.T) {
	service, err := wd.New(wd.Options{HTTPClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return nil, errors.New("secret-token-response") })}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Inspect(context.Background(), wd.SourceRef{DocumentID: "doc"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error %v", err)
	}
}

func TestReconnectInvalidatesSourcesAndPendingCallbacksAcrossServices(t *testing.T) {
	ctx := context.Background()
	store := &credentials{records: map[string]wd.CredentialRecord{"scope": {Revision: "old", ConnectionID: "old", Token: &oauth2.Token{AccessToken: "old", RefreshToken: "old", Expiry: time.Now().Add(-time.Hour)}}}}
	refreshStarted, release := make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		data := `{"access_token":"new","refresh_token":"new","expires_in":3600,"token_type":"Bearer"}`
		if r.Form.Get("grant_type") == "refresh_token" {
			close(refreshStarted)
			<-release
			data = `{"access_token":"old-refreshed","refresh_token":"old","expires_in":3600,"token_type":"Bearer"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(data))}, nil
	})}
	options := wd.AuthOptions{ClientID: "client", RedirectURL: "http://127.0.0.1/callback", Store: store, HTTPClient: client}
	a, _ := wd.NewAuth(options)
	b, _ := wd.NewAuth(options)
	source, err := a.TokenSource(ctx, "scope")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := a.Start(ctx, "scope")
	if err != nil {
		t.Fatal(err)
	}
	outcome := make(chan error, 1)
	go func() { _, err := source.Token(); outcome <- err }()
	<-refreshStarted
	start, _ := b.Start(ctx, "scope")
	if _, err = b.Finish(ctx, wd.AuthFinish{AccountRef: "scope", State: start.State, Code: "new"}); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-outcome; err == nil {
		t.Fatal("in-flight old refresh succeeded after reconnect")
	}
	if _, err = source.Token(); err == nil {
		t.Fatal("old source remains valid")
	}
	if _, err = a.Finish(ctx, wd.AuthFinish{AccountRef: "scope", State: stale.State, Code: "stale"}); err == nil {
		t.Fatal("old callback replaced new connection")
	}
	record, _ := store.Load(ctx, "scope")
	if record.Token.AccessToken != "new" || record.ConnectionID == "old" {
		t.Fatal("new connection was overwritten")
	}
}

func TestConcurrentRefreshUsesWinningCredentials(t *testing.T) {
	ctx := context.Background()
	store := &credentials{records: map[string]wd.CredentialRecord{"scope": {Revision: "old", ConnectionID: "same", Token: &oauth2.Token{RefreshToken: "old", Expiry: time.Now().Add(-time.Hour)}}}}
	var mu sync.Mutex
	calls := 0
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		started <- struct{}{}
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"access_token":"refreshed-%d","refresh_token":"rotated-%d","expires_in":3600,"token_type":"Bearer"}`, call, call)))}, nil
	})}
	options := wd.AuthOptions{ClientID: "client", RedirectURL: "http://127.0.0.1/callback", Store: store, HTTPClient: client}
	a, _ := wd.NewAuth(options)
	b, _ := wd.NewAuth(options)
	first, _ := a.TokenSource(ctx, "scope")
	second, _ := b.TokenSource(ctx, "scope")
	type result struct {
		token *oauth2.Token
		err   error
	}
	outcome := make(chan result, 2)
	for _, source := range []oauth2.TokenSource{first, second} {
		go func() { token, err := source.Token(); outcome <- result{token, err} }()
	}
	<-started
	<-started
	close(release)
	var tokens []*oauth2.Token
	for range 2 {
		received := <-outcome
		if received.err != nil {
			t.Fatal(received.err)
		}
		tokens = append(tokens, received.token)
	}

	record, _ := store.Load(ctx, "scope")
	if record.ConnectionID != "same" || record.Revision == "old" || !strings.HasPrefix(record.Token.RefreshToken, "rotated-") {
		t.Fatal("refresh lost connection identity or rotated credentials")
	}
	for _, token := range tokens {
		if token.AccessToken != record.Token.AccessToken || token.RefreshToken != record.Token.RefreshToken {
			t.Fatal("concurrent refresh did not return stored winner")
		}
	}
}
