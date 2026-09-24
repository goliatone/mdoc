package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	callbackPath   = "/oauth2/callback"
	defaultExpiry  = 2 * time.Minute
	networkTimeout = 30 * time.Second
	apiTimeout     = 2 * time.Minute
)

type Browser interface {
	Open(string) error
}

type browserFunc func(string) error

func (f browserFunc) Open(target string) error {
	return f(target)
}

type AccountResolver func(context.Context, *http.Client) (string, error)

type ServiceOptions struct {
	Store           *TokenStore
	Getenv          func(string) string
	Browser         Browser
	Random          io.Reader
	Endpoint        oauth2.Endpoint
	HTTPClient      *http.Client
	CallbackTimeout time.Duration
	PromptWriter    io.Writer
	AccountResolver AccountResolver
}

type Service struct {
	store           *TokenStore
	getenv          func(string) string
	browser         Browser
	random          io.Reader
	endpoint        oauth2.Endpoint
	httpClient      *http.Client
	callbackTimeout time.Duration
	promptWriter    io.Writer
	accountResolver AccountResolver
	legacyWarning   sync.Once
}

type LoginResult struct {
	TokenPath string
	ExpiresAt time.Time
}

type Status struct {
	Configured  bool
	Missing     []string
	SignedIn    bool
	Refreshable bool
	Account     string
	ExpiresAt   time.Time
	TokenPath   string
}

func NewService(options ServiceOptions) (*Service, error) {
	if options.Store == nil {
		path, err := DefaultTokenPath()
		if err != nil {
			return nil, err
		}
		options.Store = NewTokenStore(path)
	}
	if options.Getenv == nil {
		options.Getenv = os.Getenv
	}
	if options.Browser == nil {
		options.Browser = browserFunc(openBrowser)
	}
	if options.Endpoint.AuthURL == "" || options.Endpoint.TokenURL == "" {
		options.Endpoint = googleEndpoint
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: networkTimeout}
	}
	if options.CallbackTimeout <= 0 {
		options.CallbackTimeout = defaultExpiry
	}
	if options.PromptWriter == nil {
		options.PromptWriter = io.Discard
	}
	if options.AccountResolver == nil {
		options.AccountResolver = resolveDriveAccount
	}
	return &Service{
		store:           options.Store,
		getenv:          options.Getenv,
		browser:         options.Browser,
		random:          options.Random,
		endpoint:        options.Endpoint,
		httpClient:      options.HTTPClient,
		callbackTimeout: options.CallbackTimeout,
		promptWriter:    options.PromptWriter,
		accountResolver: options.AccountResolver,
	}, nil
}

func (s *Service) Login(ctx context.Context) (LoginResult, error) {
	credentials, err := s.credentials()
	if err != nil {
		return LoginResult{}, err
	}
	attempt, err := newOAuthAttempt(s.random)
	if err != nil {
		return LoginResult{}, err
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return LoginResult{}, fmt.Errorf("start OAuth callback listener: %w", err)
	}
	defer listener.Close()

	redirectURL := "http://" + listener.Addr().String() + callbackPath
	config := oauthConfig(credentials, s.endpoint, redirectURL)
	authorizationURL := config.AuthCodeURL(
		attempt.State,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.SetAuthURLParam("code_challenge", attempt.CodeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)

	callbackResults := make(chan callbackResult, 1)
	server := callbackServer(attempt.State, callbackResults)
	serveErrors := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
		}
	}()
	defer shutdownServer(server)

	if err := s.browser.Open(authorizationURL); err != nil {
		fmt.Fprintf(s.promptWriter, "Could not open the browser. Open this authorization URL:\n%s\n", authorizationURL)
	}

	waitContext, cancel := context.WithTimeout(ctx, s.callbackTimeout)
	defer cancel()

	var callback callbackResult
	select {
	case callback = <-callbackResults:
		if callback.Err != nil {
			return LoginResult{}, callback.Err
		}
	case err := <-serveErrors:
		return LoginResult{}, fmt.Errorf("serve OAuth callback: %w", err)
	case <-waitContext.Done():
		if errors.Is(waitContext.Err(), context.DeadlineExceeded) {
			return LoginResult{}, errors.New("OAuth login timed out waiting for the browser callback")
		}
		return LoginResult{}, fmt.Errorf("OAuth login canceled: %w", waitContext.Err())
	}

	exchangeContext := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	token, err := config.Exchange(exchangeContext, callback.Code, oauth2.VerifierOption(attempt.Verifier))
	if err != nil {
		return LoginResult{}, fmt.Errorf("exchange OAuth authorization code: %w", err)
	}
	if token.RefreshToken == "" {
		return LoginResult{}, errors.New("google returned no refresh token; remove the app grant and sign in again")
	}
	if err := s.store.Save(token); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{TokenPath: s.store.Path(), ExpiresAt: token.Expiry}, nil
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	result := Status{TokenPath: s.store.Path()}
	credentials, credentialsErr := s.credentials()
	if credentialsErr == nil {
		result.Configured = true
	} else {
		var missing *MissingCredentialsError
		if errors.As(credentialsErr, &missing) {
			result.Missing = append([]string(nil), missing.Names...)
		} else {
			return Status{}, credentialsErr
		}
	}

	token, err := s.store.Load()
	if errors.Is(err, ErrTokenNotFound) {
		return result, nil
	}
	if err != nil {
		return Status{}, err
	}
	result.SignedIn = true
	result.Refreshable = token.RefreshToken != ""
	result.ExpiresAt = token.Expiry
	if !result.Configured {
		return result, nil
	}

	config, refreshed, refreshContext, err := s.refreshToken(ctx, credentials, token)
	if err != nil {
		return Status{}, err
	}
	result.ExpiresAt = refreshed.Expiry
	result.Refreshable = refreshed.RefreshToken != ""

	httpClient := config.Client(refreshContext, refreshed)
	httpClient.Timeout = networkTimeout
	account, err := s.accountResolver(ctx, httpClient)
	if err != nil {
		return Status{}, fmt.Errorf("read signed in Google account: %w", err)
	}
	result.Account = account
	return result, nil
}

func (s *Service) Client(ctx context.Context) (*http.Client, error) {
	credentials, err := s.credentials()
	if err != nil {
		return nil, err
	}
	token, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	config, refreshed, refreshContext, err := s.refreshToken(ctx, credentials, token)
	if err != nil {
		return nil, err
	}
	client := config.Client(refreshContext, refreshed)
	client.Timeout = apiTimeout
	return client, nil
}

func (s *Service) credentials() (Credentials, error) {
	credentials, legacy, err := credentialsFromEnvWithFallback(s.getenv)
	if len(legacy) > 0 {
		s.legacyWarning.Do(func() {
			fmt.Fprintf(s.promptWriter, "Warning: deprecated OAuth environment variables are in use: %s. Set %s and %s instead.\n", strings.Join(legacy, ", "), ClientIDEnv, ClientSecretEnv)
		})
	}
	return credentials, err
}

func (s *Service) Account(ctx context.Context) (string, error) {
	client, err := s.Client(ctx)
	if err != nil {
		return "", err
	}
	account, err := s.accountResolver(ctx, client)
	if err != nil {
		return "", fmt.Errorf("read signed in Google account: %w", err)
	}
	if strings.TrimSpace(account) == "" {
		return "", errors.New("signed in Google account has no displayable identity")
	}
	return account, nil
}

func (s *Service) Logout(context.Context) error {
	return s.store.Remove()
}

func (s *Service) refreshToken(ctx context.Context, credentials Credentials, token *oauth2.Token) (*oauth2.Config, *oauth2.Token, context.Context, error) {
	config := oauthConfig(credentials, s.endpoint, "")
	refreshContext := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	refreshed, err := config.TokenSource(refreshContext, token).Token()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("refresh OAuth token: %w", err)
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = token.RefreshToken
	}
	if tokenChanged(token, refreshed) {
		if err := s.store.Save(refreshed); err != nil {
			return nil, nil, nil, err
		}
	}
	return config, refreshed, refreshContext, nil
}

type callbackResult struct {
	Code string
	Err  error
}

func callbackServer(expectedState string, results chan<- callbackResult) *http.Server {
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(writer, "Method not allowed.", http.StatusMethodNotAllowed)
			return
		}
		query := request.URL.Query()
		if oauthError := query.Get("error"); oauthError != "" {
			http.Error(writer, "Google authorization was not completed.", http.StatusBadRequest)
			once.Do(func() {
				results <- callbackResult{Err: fmt.Errorf("google authorization failed: %s", safeOAuthError(oauthError))}
			})
			return
		}
		actualState := query.Get("state")
		if actualState == "" || subtle.ConstantTimeCompare([]byte(actualState), []byte(expectedState)) != 1 {
			http.Error(writer, "Invalid OAuth state.", http.StatusBadRequest)
			once.Do(func() { results <- callbackResult{Err: errors.New("OAuth callback state did not match")} })
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(writer, "Missing authorization code.", http.StatusBadRequest)
			once.Do(func() {
				results <- callbackResult{Err: errors.New("OAuth callback did not include an authorization code")}
			})
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(writer, "Google authorization completed. You can close this window.\n")
		once.Do(func() { results <- callbackResult{Code: code} })
	})
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       5 * time.Second,
	}
}

func shutdownServer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func safeOAuthError(value string) string {
	for _, allowed := range []string{"access_denied", "admin_policy_enforced", "org_internal", "temporarily_unavailable"} {
		if value == allowed {
			return value
		}
	}
	return "authorization_error"
}

func tokenChanged(before, after *oauth2.Token) bool {
	return before.AccessToken != after.AccessToken ||
		before.RefreshToken != after.RefreshToken ||
		before.TokenType != after.TokenType ||
		!before.Expiry.Equal(after.Expiry)
}

func openBrowser(target string) error {
	command := exec.Command("open", target)
	if err := command.Start(); err != nil {
		return fmt.Errorf("open default browser: %w", err)
	}
	return nil
}

func resolveDriveAccount(ctx context.Context, client *http.Client) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/drive/v3/about?fields=user(displayName,emailAddress)", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("drive about request returned %s", response.Status)
	}
	var payload struct {
		User struct {
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
		} `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	if payload.User.EmailAddress != "" {
		return payload.User.EmailAddress, nil
	}
	return strings.TrimSpace(payload.User.DisplayName), nil
}
