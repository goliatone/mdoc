package workingdraft

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

type CredentialRecord struct {
	Revision     string        `json:"-"`
	ConnectionID string        `json:"-"`
	Token        *oauth2.Token `json:"-"`
}

// Load returns an empty record for an absent account. Revisions must never be reused.
// CompareAndSwap atomically replaces only the expected revision across all callers.
type CredentialStore interface {
	Load(ctx context.Context, accountRef string) (CredentialRecord, error)
	CompareAndSwap(ctx context.Context, accountRef, expectedRevision string, next CredentialRecord) (bool, error)
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
