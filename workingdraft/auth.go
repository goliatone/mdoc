package workingdraft

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/goliatone/mdoc/internal/auth"
	"golang.org/x/oauth2"
)

type authAttempt struct {
	account, verifier string
	expires           time.Time
}
type Auth struct {
	config   *oauth2.Config
	store    CredentialStore
	client   *http.Client
	mu       sync.Mutex
	attempts map[string]authAttempt
}

func NewAuth(o AuthOptions) (*Auth, error) {
	u, err := url.Parse(o.RedirectURL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || !(u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) || o.ClientID == "" || o.Store == nil {
		return nil, fail(AccountUnavailable, "OAuth client, trusted callback URL and credential store are required")
	}
	client := http.Client{Timeout: 30 * time.Second}
	if o.HTTPClient != nil {
		client = *o.HTTPClient
	}
	client.Timeout = 30 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Auth{config: auth.WorkingDraftConfig(o.ClientID, o.ClientSecret, o.RedirectURL), store: o.Store, client: &client, attempts: map[string]authAttempt{}}, nil
}
func (a *Auth) Start(ctx context.Context, accountRef string) (AuthStart, error) {
	if ctx.Err() != nil {
		return AuthStart{}, safeError(ctx.Err())
	}
	if accountRef == "" {
		return AuthStart{}, fail(AccountUnavailable, "host account reference is required")
	}
	state, verifier, challenge, err := auth.WorkingDraftAttempt()
	if err != nil {
		return AuthStart{}, fail(AccountUnavailable, "cannot create OAuth transaction")
	}
	expires := time.Now().Add(10 * time.Minute)
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, value := range a.attempts {
		if time.Now().After(value.expires) {
			delete(a.attempts, key)
		}
	}
	if len(a.attempts) >= 128 {
		return AuthStart{}, fail(AccountUnavailable, "too many pending OAuth transactions")
	}
	a.attempts[state] = authAttempt{accountRef, verifier, expires}
	return AuthStart{State: state, ExpiresAt: expires, AuthorizationURL: a.config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"), oauth2.SetAuthURLParam("code_challenge", challenge), oauth2.SetAuthURLParam("code_challenge_method", "S256"))}, nil
}
func (a *Auth) Finish(ctx context.Context, input AuthFinish) (AuthStatus, error) {
	a.mu.Lock()
	attempt, ok := a.attempts[input.State]
	if ok {
		delete(a.attempts, input.State)
	}
	a.mu.Unlock()
	if !ok || time.Now().After(attempt.expires) || subtle.ConstantTimeCompare([]byte(input.AccountRef), []byte(attempt.account)) != 1 || input.Code == "" {
		return AuthStatus{}, fail(AccountUnavailable, "invalid or expired OAuth transaction")
	}
	token, err := a.config.Exchange(context.WithValue(ctx, oauth2.HTTPClient, a.client), input.Code, oauth2.VerifierOption(attempt.verifier))
	if err != nil {
		return AuthStatus{}, fail(AccountUnavailable, "Google authorization exchange failed")
	}
	if token.AccessToken == "" || token.RefreshToken == "" {
		return AuthStatus{}, fail(AccountUnavailable, "Google did not grant offline access; reconnect with consent")
	}
	if err := a.store.Save(ctx, input.AccountRef, token); err != nil {
		return AuthStatus{}, fail(AccountUnavailable, "cannot save account credentials")
	}
	return tokenStatus(token), nil
}
func (a *Auth) Status(ctx context.Context, accountRef string) (AuthStatus, error) {
	t, err := a.store.Load(ctx, accountRef)
	if err != nil {
		return AuthStatus{}, fail(AccountUnavailable, "account credentials unavailable")
	}
	return tokenStatus(t), nil
}
func tokenStatus(t *oauth2.Token) AuthStatus {
	if t == nil {
		return AuthStatus{}
	}
	return AuthStatus{Connected: t.Valid() || t.RefreshToken != "", Refreshable: t.RefreshToken != "", ExpiresAt: t.Expiry}
}
func (a *Auth) TokenSource(ctx context.Context, accountRef string) (oauth2.TokenSource, error) {
	if accountRef == "" {
		return nil, fail(AccountUnavailable, "host account reference is required")
	}
	t, err := a.store.Load(ctx, accountRef)
	if err != nil || t == nil {
		return nil, fail(AccountUnavailable, "account credentials unavailable")
	}
	return &storedTokens{source: a.config.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, a.client), t), ctx: ctx, store: a.store, account: accountRef, last: *t}, nil
}

type storedTokens struct {
	mu      sync.Mutex
	source  oauth2.TokenSource
	ctx     context.Context
	store   CredentialStore
	account string
	last    oauth2.Token
}

func (s *storedTokens) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.source.Token()
	if err != nil {
		return nil, fail(AccountUnavailable, "account token refresh failed")
	}
	if t.AccessToken != s.last.AccessToken || t.RefreshToken != s.last.RefreshToken || !t.Expiry.Equal(s.last.Expiry) {
		if err := s.store.Save(s.ctx, s.account, t); err != nil {
			return nil, fail(AccountUnavailable, "cannot save refreshed credentials")
		}
		s.last = *t
	}
	copy := *t
	return &copy, nil
}
