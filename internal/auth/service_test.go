package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestLoginUsesLoopbackPKCEAndStoresToken(t *testing.T) {
	var tokenRequests atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenRequests.Add(1)
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		if request.Form.Get("code_verifier") == "" {
			t.Error("missing PKCE verifier")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"access_token":"access-value","refresh_token":"refresh-value","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	store := NewTokenStore(filepath.Join(t.TempDir(), "auth", "token.json"))
	var authorizationURL string
	browser := browserFunc(func(target string) error {
		authorizationURL = target
		parsed, err := url.Parse(target)
		if err != nil {
			return err
		}
		query := parsed.Query()
		redirect := query.Get("redirect_uri")
		callback, err := url.Parse(redirect)
		if err != nil {
			return err
		}
		callbackQuery := callback.Query()
		callbackQuery.Set("state", query.Get("state"))
		callbackQuery.Set("code", "authorization-code")
		callback.RawQuery = callbackQuery.Encode()
		go func() {
			response, callbackErr := http.Get(callback.String())
			if callbackErr == nil {
				_ = response.Body.Close()
			}
		}()
		return nil
	})
	service := newTestService(t, ServiceOptions{
		Store:      store,
		Browser:    browser,
		Endpoint:   oauth2.Endpoint{AuthURL: tokenServer.URL + "/authorize", TokenURL: tokenServer.URL},
		HTTPClient: tokenServer.Client(),
	})

	result, err := service.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.TokenPath != store.Path() {
		t.Fatalf("token path = %q", result.TokenPath)
	}
	if tokenRequests.Load() == 0 {
		t.Fatal("token endpoint was not called")
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("scope") != DriveFileScope {
		t.Fatalf("scope = %q", query.Get("scope"))
	}
	if query.Get("access_type") != "offline" || query.Get("prompt") != "consent" {
		t.Fatalf("offline consent parameters missing: %s", parsed.RawQuery)
	}
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("PKCE parameters missing: %s", parsed.RawQuery)
	}
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	if redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" || redirect.Path != callbackPath {
		t.Fatalf("redirect URI = %q", redirect.String())
	}
	if strings.Contains(authorizationURL, "secret-value") {
		t.Fatal("client secret leaked into authorization URL")
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "access-value" || loaded.RefreshToken != "refresh-value" {
		t.Fatal("stored token did not match the token response")
	}
}

func TestLoginMissingCredentialsDoesNotOpenBrowser(t *testing.T) {
	var opened atomic.Bool
	service := newTestService(t, ServiceOptions{
		Getenv: func(string) string { return "" },
		Browser: browserFunc(func(string) error {
			opened.Store(true)
			return nil
		}),
	})
	_, err := service.Login(context.Background())
	var missing *MissingCredentialsError
	if !errors.As(err, &missing) {
		t.Fatalf("login error = %v", err)
	}
	if opened.Load() {
		t.Fatal("browser opened without credentials")
	}
}

func TestLoginRejectsMismatchedState(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	service := newTestService(t, ServiceOptions{
		Store: store,
		Browser: browserFunc(func(target string) error {
			parsed, err := url.Parse(target)
			if err != nil {
				return err
			}
			callback, err := url.Parse(parsed.Query().Get("redirect_uri"))
			if err != nil {
				return err
			}
			query := callback.Query()
			query.Set("state", "wrong-state")
			query.Set("code", "unused")
			callback.RawQuery = query.Encode()
			go func() {
				response, callbackErr := http.Get(callback.String())
				if callbackErr == nil {
					_ = response.Body.Close()
				}
			}()
			return nil
		}),
	})
	_, err := service.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "state did not match") {
		t.Fatalf("login error = %v", err)
	}
	if _, loadErr := store.Load(); !errors.Is(loadErr, ErrTokenNotFound) {
		t.Fatalf("token created after bad state: %v", loadErr)
	}
}

func TestLoginTimesOut(t *testing.T) {
	service := newTestService(t, ServiceOptions{
		Browser:         browserFunc(func(string) error { return nil }),
		CallbackTimeout: 20 * time.Millisecond,
	})
	_, err := service.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("login error = %v", err)
	}
}

func TestBrowserFailurePrintsFallbackWithoutSecret(t *testing.T) {
	var prompt strings.Builder
	service := newTestService(t, ServiceOptions{
		Browser:         browserFunc(func(string) error { return errors.New("no browser") }),
		PromptWriter:    &prompt,
		CallbackTimeout: 20 * time.Millisecond,
	})
	_, _ = service.Login(context.Background())
	if !strings.Contains(prompt.String(), "/authorize?") {
		t.Fatalf("fallback prompt = %q", prompt.String())
	}
	if strings.Contains(prompt.String(), "secret-value") {
		t.Fatal("client secret leaked into fallback prompt")
	}
}

func TestStatusRefreshPreservesRefreshToken(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		if request.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant type = %q", request.Form.Get("grant_type"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"access_token":"new-access","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	if err := store.Save(&oauth2.Token{
		AccessToken:  "expired-access",
		RefreshToken: "keep-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, ServiceOptions{
		Store:      store,
		Endpoint:   oauth2.Endpoint{AuthURL: tokenServer.URL + "/authorize", TokenURL: tokenServer.URL},
		HTTPClient: tokenServer.Client(),
		AccountResolver: func(context.Context, *http.Client) (string, error) {
			return "author@example.test", nil
		},
	})
	result, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Account != "author@example.test" || !result.Refreshable {
		t.Fatalf("status = %+v", result)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "new-access" || loaded.RefreshToken != "keep-refresh" {
		t.Fatalf("refreshed token = %+v", loaded)
	}
}

func TestStatusWithoutEnvironmentDoesNotExposeToken(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	if err := store.Save(&oauth2.Token{AccessToken: "access-value", RefreshToken: "refresh-value"}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, ServiceOptions{
		Store:  store,
		Getenv: func(string) string { return "" },
	})
	result, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "access-value") || strings.Contains(string(encoded), "refresh-value") {
		t.Fatal("status exposed token material")
	}
}

func TestClientAllowsLargeDocumentExports(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	if err := store.Save(&oauth2.Token{AccessToken: "access-value", RefreshToken: "refresh-value", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, ServiceOptions{Store: store})
	client, err := service.Client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != apiTimeout {
		t.Fatalf("API client timeout = %s, expected %s", client.Timeout, apiTimeout)
	}
}

func newTestService(t *testing.T, options ServiceOptions) *Service {
	t.Helper()
	if options.Store == nil {
		options.Store = NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	}
	if options.Getenv == nil {
		options.Getenv = func(name string) string {
			switch name {
			case ClientIDEnv:
				return "client-id"
			case ClientSecretEnv:
				return "secret-value"
			default:
				return ""
			}
		}
	}
	if options.Browser == nil {
		options.Browser = browserFunc(func(string) error { return nil })
	}
	if options.Endpoint.AuthURL == "" {
		options.Endpoint = oauth2.Endpoint{AuthURL: "https://example.test/authorize", TokenURL: "https://example.test/token"}
	}
	if options.CallbackTimeout == 0 {
		options.CallbackTimeout = time.Second
	}
	service, err := NewService(options)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func ExampleMissingCredentialsError() {
	err := &MissingCredentialsError{Names: []string{ClientIDEnv}}
	fmt.Println(err)
	// Output: missing OAuth environment variables: MDOC_GOOGLE_CLIENT_ID
}
