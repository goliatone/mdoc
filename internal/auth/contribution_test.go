package auth

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type contributionTransport func(*http.Request) (*http.Response, error)

func (f contributionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestContributionConnectionInvalidatesEveryUse(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "auth", "token.json"))
	token := &oauth2.Token{AccessToken: "first", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	if e := store.Save(token); e != nil {
		t.Fatal(e)
	}
	calls := 0
	service := newTestService(t, ServiceOptions{Store: store, HTTPClient: &http.Client{Transport: contributionTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}})
	client, binding, e := service.ContributionClient(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	resp, e := client.Get("https://example.test/")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	before, _ := store.Load()
	refreshed := *before
	refreshed.AccessToken = "refreshed"
	if e = store.saveRefresh(before, &refreshed); e != nil {
		t.Fatal(e)
	}
	_, same, e := service.ContributionClient(context.Background())
	if e != nil || same != binding {
		t.Fatal("refresh changed connection")
	}
	if e = store.Save(token); e != nil {
		t.Fatal(e)
	}
	_, changed, e := service.ContributionClient(context.Background())
	if e != nil || changed == binding {
		t.Fatal("reconnect preserved identity")
	}
	if _, e = client.Get("https://example.test/"); e == nil || calls != 1 {
		t.Fatal("cached token survived reconnect")
	}
	if e = store.saveRefresh(before, &refreshed); e == nil {
		t.Fatal("stale refresh overwrote reconnect")
	}
	current, _ := store.Load()
	if current.AccessToken != "first" {
		t.Fatal("reconnect overwritten")
	}
}
