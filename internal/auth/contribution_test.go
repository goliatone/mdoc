package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Legacy credentials must acquire a persisted identity before any plan/client can
// bind to them, even when there is no refresh token to distinguish accounts.
func TestContributionLegacyConnectionMigration(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store := NewTokenStore(filepath.Join(dir, "token.json"))
	writeLegacy := func(access string) {
		t.Helper()
		b, err := json.Marshal(&oauth2.Token{AccessToken: access, TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(store.Path(), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeLegacy("account-A")
	// Independent stores concurrently admit the same legacy file only once.
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := NewTokenStore(store.Path()).Load()
			if err != nil {
				t.Error(err)
				return
			}
			ids <- connectionID(token)
		}()
	}
	wg.Wait()
	close(ids)
	var generation string
	for id := range ids {
		if id == "" {
			t.Fatal("missing generation")
		}
		if generation != "" && generation != id {
			t.Fatal("concurrent migration split identity")
		}
		generation = id
	}
	b, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Connection string `json:"mdoc_connection_id"`
	}
	if err = json.Unmarshal(b, &record); err != nil || record.Connection != generation {
		t.Fatal("generation not persisted", err)
	}
	calls := 0
	svc := newTestService(t, ServiceOptions{Store: store, HTTPClient: &http.Client{Transport: contributionTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}})
	old, first, err := svc.ContributionClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	writeLegacy("account-B")
	_, second, err := svc.ContributionClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("replacement legacy credentials reused binding")
	}
	if resp, err := old.Get("https://example.test/"); err == nil {
		resp.Body.Close()
		t.Fatal("old client accepted replacement account")
	}
	if calls != 0 {
		t.Fatal("old client transmitted")
	}
	if err = store.saveRefresh(before, before); err == nil {
		t.Fatal("stale refresh overwrote replacement")
	}
	current, err := store.Load()
	if err != nil || current.AccessToken != "account-B" {
		t.Fatal("replacement lost", err)
	}
}
