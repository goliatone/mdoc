package auth

import "golang.org/x/oauth2"

func WorkingDraftConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return oauthConfig(Credentials{ClientID: clientID, ClientSecret: clientSecret}, googleEndpoint, redirectURL)
}
func WorkingDraftAttempt() (state, verifier, challenge string, err error) {
	a, err := newOAuthAttempt(nil)
	return a.State, a.Verifier, a.CodeChallenge, err
}
