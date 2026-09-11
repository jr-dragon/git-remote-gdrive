package googleauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestAuthorize(t *testing.T) {
	for _, mode := range []string{"callback", "manual", "listen-failure", "denied", "exchange-failure", "missing-refresh", "missing-scope"} {
		t.Run(mode, func(t *testing.T) {
			var challenge, redirect string
			exchanges := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				exchanges++
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				v := r.Form.Get("code_verifier")
				hash := sha256.Sum256([]byte(v))
				if v == "" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
					t.Error("PKCE verifier did not match challenge")
				}
				if r.Form.Get("redirect_uri") != redirect || r.Form.Get("code") != "test-code" || r.Form.Get("client_id") != "client" || r.Form.Get("client_secret") != "secret" || r.Form.Get("grant_type") != "authorization_code" {
					t.Error("incorrect exchange parameters")
				}
				w.Header().Set("Content-Type", "application/json")
				if mode == "exchange-failure" {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":"invalid_grant","error_description":"sensitive-test-code"}`)
					return
				}
				refresh, scope := "refresh", DriveScope
				if mode == "missing-refresh" {
					refresh = ""
				}
				if mode == "missing-scope" {
					scope = "openid"
				}
				fmt.Fprintf(w, `{"access_token":"access","refresh_token":%q,"scope":%q,"token_type":"Bearer","expires_in":3600}`, refresh, scope)
			}))
			defer ts.Close()
			config := &oauth2.Config{ClientID: "client", ClientSecret: "secret", Scopes: []string{DriveScope}, Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.example/auth", TokenURL: ts.URL, AuthStyle: oauth2.AuthStyleInParams}}
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			listen := net.Listen
			if mode == "listen-failure" {
				listen = func(string, string) (net.Listener, error) { return nil, errors.New("unavailable") }
			}
			opts := Options{Manual: mode != "callback" && mode != "listen-failure", Input: reader, OpenBrowser: func(raw string) error {
				u, _ := url.Parse(raw)
				q := u.Query()
				challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
				if q.Get("code_challenge_method") != "S256" || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("scope") != DriveScope {
					t.Error("incorrect authorization parameters")
				}
				callback := redirect + "?" + url.Values{"state": {q.Get("state")}, "code": {"test-code"}}.Encode()
				if mode == "denied" {
					callback = redirect + "?" + url.Values{"state": {q.Get("state")}, "error": {"access_denied"}}.Encode()
				}
				if mode == "callback" {
					bad, err := http.Get(redirect + "?state=wrong&code=attacker")
					if err != nil {
						return err
					}
					bad.Body.Close()
					if bad.StatusCode != 400 {
						t.Error("invalid state accepted")
					}
					res, err := http.Get(callback)
					if err != nil {
						return err
					}
					res.Body.Close()
				} else {
					go func() { fmt.Fprintln(writer, callback) }()
				}
				return nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := authorize(ctx, config, opts, listen)
			wantError := mode == "denied" || mode == "exchange-failure" || mode == "missing-refresh" || mode == "missing-scope"
			if wantError {
				if err == nil {
					t.Fatal("expected failure")
				}
				if strings.Contains(err.Error(), "sensitive-test-code") {
					t.Fatal("token response leaked")
				}
			} else if err != nil {
				t.Fatal(err)
			} else if c.Token.RefreshToken != "refresh" || c.Token.Expiry.Before(time.Now()) {
				t.Fatal("invalid saved token")
			}
			if mode == "denied" && exchanges != 0 {
				t.Fatal("exchanged denied authorization")
			}
		})
	}
}

func TestParseCallback(t *testing.T) {
	redirect := "http://127.0.0.1:1234/oauth2/callback"
	for _, raw := range []string{"code-only", redirect + "?code=a", redirect + "?code=a&state=bad", redirect + "?code=a&state=s&state=s", redirect + "?code=a&code=b&state=s", "http://evil.example/oauth2/callback?code=a&state=s", redirect + "?error=access_denied&state=s", redirect + "?code=a&state=s#fragment"} {
		if parseCallback(raw, redirect, "s").err == nil {
			t.Errorf("accepted invalid callback %q", raw)
		}
	}
	if result := parseCallback(redirect+"?code=a%2Bb&state=s", redirect, "s"); result.err != nil || result.code != "a+b" {
		t.Fatal("valid callback rejected")
	}
}

func TestCanceledAuthorization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := authorize(ctx, &oauth2.Config{Endpoint: googleEndpoint}, Options{Manual: true}, net.Listen)
	if err == nil {
		t.Fatal("expected cancellation")
	}
}
