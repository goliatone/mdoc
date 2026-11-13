package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// ContributionClient binds the CLI service to this persisted login generation.
// Every token use checks that logout/reconnect has not replaced that generation.
func (s *Service) ContributionClient(ctx context.Context) (*http.Client, string, error) {
	credentials, e := s.credentials()
	if e != nil {
		return nil, "", e
	}
	token, e := s.store.Load()
	if e != nil {
		return nil, "", e
	}
	connection := connectionID(token)
	c := *s.httpClient
	if c.Timeout <= 0 || c.Timeout > 30*time.Second {
		c.Timeout = 30 * time.Second
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	refreshContext := context.WithValue(ctx, oauth2.HTTPClient, &c)
	source := &connectionSource{store: s.store, connection: connection, config: oauthConfig(credentials, s.endpoint, ""), ctx: refreshContext}
	client := &http.Client{Transport: &oauth2.Transport{Source: source, Base: c.Transport}}
	client.Timeout = 30 * time.Second
	client.CheckRedirect = c.CheckRedirect
	sum := sha256.Sum256([]byte("mdoc.cli.connection.v1\x00" + credentials.ClientID + "\x00" + connection))
	return client, hex.EncodeToString(sum[:]), nil
}

type connectionSource struct {
	mu         sync.Mutex
	store      *TokenStore
	connection string
	config     *oauth2.Config
	ctx        context.Context
}

func (s *connectionSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before, e := s.store.Load()
	if e != nil || connectionID(before) != s.connection {
		return nil, errors.New("account connection changed")
	}
	next, e := s.config.TokenSource(s.ctx, before).Token()
	if e != nil {
		return nil, errors.New("account token unavailable")
	}
	if next.RefreshToken == "" {
		next.RefreshToken = before.RefreshToken
	}
	if tokenChanged(before, next) {
		if e = s.store.saveRefresh(before, next); e != nil {
			return nil, errors.New("account changed during refresh")
		}
	}
	after, e := s.store.Load()
	if e != nil || connectionID(after) != s.connection {
		return nil, errors.New("account connection changed")
	}
	return next, nil
}
