package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestTokenStoreSavesAtomicallyWithPrivatePermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	store := NewTokenStore(filepath.Join(directory, "token.json"))

	first := &oauth2.Token{AccessToken: "first", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	second := &oauth2.Token{AccessToken: "second", RefreshToken: "refresh", Expiry: time.Now().Add(2 * time.Hour)}
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}

	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %04o, want 0700", got)
	}
	fileInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("token mode = %04o, want 0600", got)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "second" {
		t.Fatalf("access token = %q", loaded.AccessToken)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "token.json" {
		t.Fatalf("unexpected token directory entries: %v", entries)
	}
}

func TestTokenStoreRejectsBroadPermissions(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	if err := os.WriteFile(store.Path(), []byte(`{"access_token":"unsafe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if err == nil {
		t.Fatal("expected a permissions error")
	}
}

func TestTokenStoreRejectsBroadDirectoryPermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "auth")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewTokenStore(filepath.Join(directory, "token.json"))
	if err := os.WriteFile(store.Path(), []byte(`{"access_token":"unsafe"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if err == nil || !strings.Contains(err.Error(), "directory permissions") {
		t.Fatalf("load error = %v", err)
	}
}

func TestTokenStoreRemoveIsIdempotent(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&oauth2.Token{AccessToken: "access"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("load after remove = %v", err)
	}
}
