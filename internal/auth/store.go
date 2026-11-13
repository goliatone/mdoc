package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
	"golang.org/x/sys/unix"
)

var ErrTokenNotFound = errors.New("OAuth token not found")

type TokenStore struct {
	path string
}

func DefaultTokenPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(configDir, "mdoc", "token.json"), nil
}

func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

func (s *TokenStore) Path() string {
	return s.path
}

func (s *TokenStore) Load() (*oauth2.Token, error) {
	info, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("inspect OAuth token: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("OAuth token permissions are too broad: got %04o, want 0600", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(s.path))
	if err != nil {
		return nil, fmt.Errorf("inspect OAuth token directory: %w", err)
	}
	if directoryInfo.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("OAuth token directory permissions are too broad: got %04o, want 0700", directoryInfo.Mode().Perm())
	}

	file, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open OAuth token: %w", err)
	}
	defer file.Close()

	var record struct {
		oauth2.Token
		ConnectionID string `json:"mdoc_connection_id,omitempty"`
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("decode OAuth token: %w", err)
	}
	token := &record.Token
	if token.AccessToken == "" && token.RefreshToken == "" {
		return nil, errors.New("OAuth token file contains no usable token")
	}
	if record.ConnectionID == "" {
		sum := sha256.Sum256([]byte("legacy-connection\x00" + token.RefreshToken + "\x00" + token.TokenType))
		record.ConnectionID = hex.EncodeToString(sum[:])
	}
	return token.WithExtra(map[string]any{"mdoc_connection_id": record.ConnectionID}), nil
}

func (s *TokenStore) write(token *oauth2.Token, connection string) error {
	if token == nil {
		return errors.New("cannot save a nil OAuth token")
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create OAuth token directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("protect OAuth token directory: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary OAuth token: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary OAuth token: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(struct {
		*oauth2.Token
		ConnectionID string `json:"mdoc_connection_id"`
	}{token, connection}); err != nil {
		return fmt.Errorf("encode OAuth token: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync OAuth token: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close OAuth token: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace OAuth token: %w", err)
	}
	committed = true

	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open OAuth token directory for sync: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync OAuth token directory: %w", err)
	}
	return nil
}

func (s *TokenStore) remove() error {
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove OAuth token: %w", err)
	}
	return nil
}

// lock serializes connection replacement and compare-and-swap refreshes.
func (s *TokenStore) lock() (func(), error) {
	if e := os.MkdirAll(filepath.Dir(s.path), 0700); e != nil {
		return nil, e
	}
	fd, e := unix.Open(s.path+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), s.path+".lock")
	if e := unix.Flock(fd, unix.LOCK_EX); e != nil {
		f.Close()
		return nil, e
	}
	return func() { unix.Flock(fd, unix.LOCK_UN); f.Close() }, nil
}
func connectionID(t *oauth2.Token) string { v, _ := t.Extra("mdoc_connection_id").(string); return v }
func (s *TokenStore) Save(token *oauth2.Token) error {
	release, e := s.lock()
	if e != nil {
		return e
	}
	defer release()
	b := make([]byte, 16)
	if _, e = rand.Read(b); e != nil {
		return e
	}
	return s.write(token, hex.EncodeToString(b))
}
func (s *TokenStore) saveRefresh(before, after *oauth2.Token) error {
	release, e := s.lock()
	if e != nil {
		return e
	}
	defer release()
	current, e := s.Load()
	if e != nil {
		return e
	}
	if connectionID(current) != connectionID(before) || tokenChanged(current, before) {
		return errors.New("account credentials changed during refresh")
	}
	return s.write(after, connectionID(current))
}
func (s *TokenStore) Remove() error {
	release, e := s.lock()
	if e != nil {
		return e
	}
	defer release()
	return s.remove()
}
