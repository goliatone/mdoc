package workingdraft

import (
	"context"
	"golang.org/x/oauth2"
	"net/http"
	"time"
)

type CredentialStore interface {
	Load(context.Context, string) (*oauth2.Token, error)
	Save(context.Context, string, *oauth2.Token) error
}
type AuthOptions struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Store        CredentialStore
	HTTPClient   *http.Client
}
type AuthStart struct {
	AuthorizationURL string    `json:"authorization_url"`
	State            string    `json:"state"`
	ExpiresAt        time.Time `json:"expires_at"`
}
type AuthFinish struct {
	AccountRef string
	State      string
	Code       string
}
type AuthStatus struct {
	Connected   bool      `json:"connected"`
	Refreshable bool      `json:"refreshable"`
	ExpiresAt   time.Time `json:"expires_at"`
}
