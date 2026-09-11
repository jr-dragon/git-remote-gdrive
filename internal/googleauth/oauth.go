package googleauth

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// DriveScope permits access to existing folders supplied by ID, without Picker.
const DriveScope = "https://www.googleapis.com/auth/drive"

var googleEndpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

type Options struct {
	ClientFile  string
	Manual      bool
	Input       io.Reader
	Output      io.Writer
	OpenBrowser func(string) error
}

func Authenticate(ctx context.Context, opts Options) (*Credential, error) {
	data, err := os.ReadFile(opts.ClientFile)
	if err != nil {
		return nil, fmt.Errorf("read OAuth client file: %w", err)
	}
	var client struct {
		Installed struct {
			ID     string `json:"client_id"`
			Secret string `json:"client_secret"`
		} `json:"installed"`
	}
	if json.Unmarshal(data, &client) != nil || client.Installed.ID == "" || client.Installed.Secret == "" {
		return nil, errors.New("expected a Google OAuth Desktop app client JSON with client_id and client_secret")
	}
	config := &oauth2.Config{ClientID: client.Installed.ID, ClientSecret: client.Installed.Secret, Endpoint: googleEndpoint, Scopes: []string{DriveScope}}
	return authorize(ctx, config, opts, net.Listen)
}

type authResult struct {
	code string
	err  error
}

func authorize(ctx context.Context, config *oauth2.Config, opts Options, listen func(string, string) (net.Listener, error)) (*Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if opts.Input == nil {
		opts.Input = strings.NewReader("")
	}
	if opts.Output == nil {
		opts.Output = io.Discard
	}
	state, verifier := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	results := make(chan authResult, 1)
	var listener net.Listener
	if !opts.Manual {
		var err error
		listener, err = listen("tcp4", "127.0.0.1:0")
		if err != nil {
			fmt.Fprintln(opts.Output, "Cannot start local callback; using manual redirect URL input.")
		}
	}
	config.RedirectURL = "http://127.0.0.1:1/oauth2/callback"
	if listener != nil {
		config.RedirectURL = "http://" + listener.Addr().String() + "/oauth2/callback"
		server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
		server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			if r.Method != http.MethodGet || r.URL.Path != "/oauth2/callback" {
				http.NotFound(w, r)
				return
			}
			callback := "http://" + r.Host + r.URL.RequestURI()
			result := parseCallback(callback, config.RedirectURL, state)
			if result.err != nil && result.code == "" {
				// An unrelated request must not cancel a valid pending login.
				if !validState(r.URL.Query(), state) {
					http.Error(w, "Invalid OAuth state.", http.StatusBadRequest)
					return
				}
			}
			select {
			case results <- result:
			default:
			}
			fmt.Fprintln(w, "Authentication response received. You can close this window and return to the terminal.")
		})
		defer server.Close()
		go func() { _ = server.Serve(listener) }()
	}
	authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"), oauth2.S256ChallengeOption(verifier))
	fmt.Fprintf(opts.Output, "Open this URL to authorize Google Drive access:\n%s\n", authURL)
	if opts.OpenBrowser != nil {
		if err := opts.OpenBrowser(authURL); err != nil {
			fmt.Fprintln(opts.Output, "Could not open the browser automatically. Open the URL above manually.")
		}
	}
	fmt.Fprintln(opts.Output, "Waiting for authorization. If the callback cannot connect, paste the entire final http://127.0.0.1:... URL from the browser address bar here, then press Enter:")
	// Only this short-lived CLI reads stdin; callback completion need not wait for input.
	input := make(chan authResult, 1)
	go func() {
		scanner := bufio.NewScanner(opts.Input)
		scanner.Buffer(make([]byte, 4096), 64*1024)
		if scanner.Scan() {
			input <- parseCallback(strings.TrimSpace(scanner.Text()), config.RedirectURL, state)
			return
		}
		input <- authResult{err: errors.New("manual authorization input closed or could not be read")}
	}()
	var result authResult
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("authorization canceled or timed out: %w", ctx.Err())
		case result = <-results:
			goto exchange
		case result = <-input:
			if result.err != nil && listener != nil {
				fmt.Fprintln(opts.Output, "Manual input was unavailable or invalid; still waiting for the browser callback.")
				input = nil
				continue
			}
			goto exchange
		}
	}
exchange:
	if result.err != nil {
		return nil, result.err
	}
	token, err := config.Exchange(ctx, result.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, errors.New("Google token exchange failed; retry git gdrive config and check your OAuth client configuration")
	}
	if scopes, ok := token.Extra("scope").(string); ok {
		granted := false
		for _, scope := range strings.Fields(scopes) {
			if scope == DriveScope {
				granted = true
			}
		}
		if !granted {
			return nil, errors.New("Google Drive permission was not granted; run git gdrive config again")
		}
	}
	c := &Credential{ClientID: config.ClientID, ClientSecret: config.ClientSecret, Token: token}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func validState(q url.Values, state string) bool {
	return len(q["state"]) == 1 && subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) == 1
}

func parseCallback(raw, redirect, state string) authResult {
	u, err := url.Parse(raw)
	expected, _ := url.Parse(redirect)
	if err != nil || u.Scheme != expected.Scheme || u.Host != expected.Host || u.Path != expected.Path || u.User != nil || u.Fragment != "" {
		return authResult{err: errors.New("paste the complete callback URL from this authorization attempt")}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || !validState(q, state) {
		return authResult{err: errors.New("OAuth state mismatch; retry git gdrive config")}
	}
	if q.Has("error") {
		return authResult{err: errors.New("Google authorization was denied or failed")}
	}
	if len(q["code"]) != 1 || q.Get("code") == "" {
		return authResult{err: errors.New("callback URL is missing an authorization code")}
	}
	return authResult{code: q.Get("code")}
}
