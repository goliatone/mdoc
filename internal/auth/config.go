package auth

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/oauth2"
)

const (
	ClientIDEnv           = "MDOC_GOOGLE_CLIENT_ID"
	ClientSecretEnv       = "MDOC_GOOGLE_CLIENT_SECRET"
	LegacyClientIDEnv     = "MEC_GOOGLE_CLIENT_ID"
	LegacyClientSecretEnv = "MEC_GOOGLE_SECRET"
	DriveFileScope        = "https://www.googleapis.com/auth/drive.file"
)

var googleEndpoint = oauth2.Endpoint{
	AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
}

type Credentials struct {
	ClientID     string
	ClientSecret string
}

type MissingCredentialsError struct {
	Names []string
}

func (e *MissingCredentialsError) Error() string {
	return fmt.Sprintf("missing OAuth environment variables: %s", strings.Join(e.Names, ", "))
}

func credentialsFromEnv(getenv func(string) string) (Credentials, error) {
	credentials, _, err := credentialsFromEnvWithFallback(getenv)
	return credentials, err
}

func credentialsFromEnvWithFallback(getenv func(string) string) (Credentials, []string, error) {
	credentials := Credentials{
		ClientID:     getenv(ClientIDEnv),
		ClientSecret: getenv(ClientSecretEnv),
	}
	legacy := []string{}
	if strings.TrimSpace(credentials.ClientID) == "" {
		credentials.ClientID = getenv(LegacyClientIDEnv)
		if strings.TrimSpace(credentials.ClientID) != "" {
			legacy = append(legacy, LegacyClientIDEnv)
		}
	}
	if strings.TrimSpace(credentials.ClientSecret) == "" {
		credentials.ClientSecret = getenv(LegacyClientSecretEnv)
		if strings.TrimSpace(credentials.ClientSecret) != "" {
			legacy = append(legacy, LegacyClientSecretEnv)
		}
	}

	missing := make([]string, 0, 2)
	if strings.TrimSpace(credentials.ClientID) == "" {
		missing = append(missing, ClientIDEnv)
	}
	if strings.TrimSpace(credentials.ClientSecret) == "" {
		missing = append(missing, ClientSecretEnv)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Credentials{}, legacy, &MissingCredentialsError{Names: missing}
	}

	return credentials, legacy, nil
}

func oauthConfig(credentials Credentials, endpoint oauth2.Endpoint, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     credentials.ClientID,
		ClientSecret: credentials.ClientSecret,
		Endpoint:     endpoint,
		RedirectURL:  redirectURL,
		Scopes:       []string{DriveFileScope},
	}
}
