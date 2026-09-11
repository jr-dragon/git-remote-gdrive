package googleauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type tokenFunc func() (*oauth2.Token, error)

func (f tokenFunc) Token() (*oauth2.Token, error) { return f() }

func TestRefreshedCredentialPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "credential")
	c := &Credential{ClientID: "client", ClientSecret: "secret", Token: &oauth2.Token{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)}}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	next := &oauth2.Token{AccessToken: "new", RefreshToken: "rotated", Expiry: time.Now().Add(time.Hour)}
	source := persistedSource{credential: c, path: path, source: tokenFunc(func() (*oauth2.Token, error) { return next, nil })}
	if _, err := source.Token(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Token.AccessToken != "new" || loaded.Token.RefreshToken != "rotated" {
		t.Fatal("refreshed credential was not saved", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new" {
			t.Error("missing OAuth authorization")
		}
		io.WriteString(w, "ok")
	}))
	defer server.Close()
	client, err := Client(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
}
