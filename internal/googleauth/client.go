package googleauth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Client loads the same credential used by config and refreshes access tokens
// without reading stdin or launching a browser.
func Client(ctx context.Context, path string) (*http.Client, error) {
	c, err := Load(path)
	if err != nil {
		return nil, err
	}
	config := &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: googleEndpoint}
	refreshCtx := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: 30 * time.Second})
	source := &persistedSource{source: config.TokenSource(refreshCtx, c.Token), credential: c, path: path}
	client := oauth2.NewClient(ctx, source)
	client.Timeout = 2 * time.Minute
	return client, nil
}

type persistedSource struct {
	mu         sync.Mutex
	source     oauth2.TokenSource
	credential *Credential
	path       string
}

func (s *persistedSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, err := s.source.Token()
	if err != nil {
		return nil, errors.New("Google credential refresh failed; run git gdrive config")
	}
	if token.AccessToken != s.credential.Token.AccessToken || token.RefreshToken != s.credential.Token.RefreshToken || !token.Expiry.Equal(s.credential.Token.Expiry) {
		next := *s.credential
		next.Token = token
		if err := Save(s.path, &next); err != nil {
			return nil, errors.New("could not save refreshed Google credential")
		}
		s.credential = &next
	}
	return token, nil
}
