package auth

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCredentialsFromEnvReportsNamesOnly(t *testing.T) {
	values := map[string]string{
		ClientIDEnv:     "",
		ClientSecretEnv: "  ",
	}
	_, err := credentialsFromEnv(func(name string) string { return values[name] })
	var missing *MissingCredentialsError
	if !errors.As(err, &missing) {
		t.Fatalf("expected MissingCredentialsError, got %v", err)
	}
	want := []string{ClientIDEnv, ClientSecretEnv}
	if !reflect.DeepEqual(missing.Names, want) {
		t.Fatalf("missing names = %v, want %v", missing.Names, want)
	}
	if got := err.Error(); got != "missing OAuth environment variables: MDOC_GOOGLE_CLIENT_ID, MDOC_GOOGLE_CLIENT_SECRET" {
		t.Fatalf("error = %q", got)
	}
}

func TestLegacyOAuthVariablesWarnOnceWithoutPrintingValues(t *testing.T) {
	values := map[string]string{
		LegacyClientIDEnv:     "sensitive-client-id",
		LegacyClientSecretEnv: "sensitive-client-secret",
	}
	var output strings.Builder
	service, err := NewService(ServiceOptions{
		Store:        NewTokenStore(filepath.Join(t.TempDir(), "token.json")),
		Getenv:       func(name string) string { return values[name] },
		PromptWriter: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		status, err := service.Status(t.Context())
		if err != nil || !status.Configured {
			t.Fatalf("status=%#v error=%v", status, err)
		}
	}
	message := output.String()
	if strings.Count(message, "deprecated OAuth") != 1 || !strings.Contains(message, LegacyClientIDEnv) || !strings.Contains(message, LegacyClientSecretEnv) {
		t.Fatalf("warning = %q", message)
	}
	if strings.Contains(message, values[LegacyClientIDEnv]) || strings.Contains(message, values[LegacyClientSecretEnv]) {
		t.Fatalf("warning leaked credentials: %q", message)
	}
}

func TestOAuthAttemptUsesPKCES256(t *testing.T) {
	input := make([]byte, 96)
	for index := range input {
		input[index] = byte(index + 1)
	}
	attempt, err := newOAuthAttempt(bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(attempt.State) < 43 {
		t.Fatalf("state too short: %d", len(attempt.State))
	}
	if len(attempt.Verifier) < 43 || len(attempt.Verifier) > 128 {
		t.Fatalf("verifier length = %d", len(attempt.Verifier))
	}
	if attempt.CodeChallenge == "" || attempt.CodeChallenge == attempt.Verifier {
		t.Fatal("expected an S256 code challenge")
	}
}
