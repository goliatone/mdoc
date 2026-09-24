package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

type oauthAttempt struct {
	State         string
	Verifier      string
	CodeChallenge string
}

func newOAuthAttempt(source io.Reader) (oauthAttempt, error) {
	state, err := randomURLValue(source, 32)
	if err != nil {
		return oauthAttempt{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	verifier, err := randomURLValue(source, 64)
	if err != nil {
		return oauthAttempt{}, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	digest := sha256.Sum256([]byte(verifier))
	return oauthAttempt{
		State:         state,
		Verifier:      verifier,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(digest[:]),
	}, nil
}

func randomURLValue(source io.Reader, size int) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	value := make([]byte, size)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
