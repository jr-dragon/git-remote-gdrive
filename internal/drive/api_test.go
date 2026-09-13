package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func backendForTest(t *testing.T, server *httptest.Server, backend string) API {
	t.Helper()
	api, err := NewAPI(context.Background(), server.Client(), backend)
	if err != nil {
		t.Fatal(err)
	}
	switch c := api.(type) {
	case *Client:
		c.BaseURL = server.URL
		c.sleep = func(context.Context, time.Duration) error { return nil }
	case *SDKV3Client:
		c.Service.BasePath = server.URL + "/drive/v3/"
		c.retry.sleep = func(context.Context, time.Duration) error { return nil }
	case *SDKV2Client:
		c.Service.BasePath = server.URL + "/drive/v2/"
		c.retry.sleep = func(context.Context, time.Duration) error { return nil }
	}
	return api
}

func TestAPISelection(t *testing.T) {
	for _, backend := range []string{"unset", "", "http", "sdkv2", "sdkv3"} {
		t.Setenv("GIT_GDRIVE_API_BACKEND", backend)
		if backend == "unset" {
			if err := os.Unsetenv("GIT_GDRIVE_API_BACKEND"); err != nil {
				t.Fatal(err)
			}
		}
		api, err := ConfiguredAPI(context.Background(), http.DefaultClient)
		if err != nil || api == nil {
			t.Fatal(backend, err)
		}
		if backend == "sdkv2" {
			if _, ok := api.(*SDKV2Client); !ok {
				t.Fatal("wrong SDK backend")
			}
		} else if backend == "unset" || backend == "" || backend == "sdkv3" {
			if _, ok := api.(*SDKV3Client); !ok {
				t.Fatal("wrong v3 SDK backend")
			}
		} else {
			if _, ok := api.(*Client); !ok {
				t.Fatal("wrong HTTP backend")
			}
		}
	}
	for _, invalid := range []string{"sdk", "typo"} {
		if _, err := NewAPI(context.Background(), http.DefaultClient, invalid); err == nil {
			t.Fatal("invalid backend accepted", invalid)
		}
	}
}

func TestAPIQuotaAndSafeErrors(t *testing.T) {
	for _, backend := range []string{"http", "sdkv2", "sdkv3"} {
		for _, tc := range []struct {
			code   int
			reason string
			retry  bool
		}{
			{429, "rateLimitExceeded", true}, {503, "backendError", true}, {403, "userRateLimitExceeded", true}, {403, "insufficientFilePermissions", false}, {412, "conditionNotMet", false},
		} {
			t.Run(fmt.Sprintf("%s/%d/%s", backend, tc.code, tc.reason), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if calls == 1 {
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(tc.code)
						fmt.Fprintf(w, `{"error":{"code":%d,"message":"SECRET_SESSION_URL","errors":[{"reason":%q}]}}`, tc.code, tc.reason)
						return
					}
					w.Header().Set("ETag", "tag")
					fmt.Fprint(w, `{"id":"root","mimeType":"application/vnd.google-apps.folder","etag":"tag"}`)
				}))
				defer server.Close()
				api := backendForTest(t, server, backend)
				f, err := api.Metadata(context.Background(), "root")
				if tc.retry {
					if err != nil || calls != 2 || f.ETag != "tag" {
						t.Fatal(calls, f, err)
					}
				} else if err == nil || calls != 1 || !statusIs(err, tc.code) || strings.Contains(err.Error(), "SECRET") {
					t.Fatal(calls, err)
				}
			})
		}
	}
}

func TestAPIPatchAndList(t *testing.T) {
	for _, backend := range []string{"http", "sdkv2", "sdkv3"} {
		t.Run(backend, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method == "PATCH" {
					if r.Header.Get("If-Match") != "expected" || r.URL.Query().Get("supportsAllDrives") != "true" {
						t.Error("lost precondition")
					}
					var fields map[string]json.RawMessage
					_ = json.NewDecoder(r.Body).Decode(&fields)
					if len(fields) != 1 || fields["properties"] == nil {
						t.Error("patch modified unrelated fields", fields)
					}
					fmt.Fprint(w, `{"id":"root"}`)
					return
				}
				if backend == "sdkv3" {
					if strings.HasSuffix(r.URL.Path, "/zip") {
						w.Header().Set("ETag", "tag")
						fmt.Fprint(w, `{"id":"zip","name":"main.zip","parents":["folder"],"mimeType":"application/zip"}`)
					} else if r.URL.Query().Get("pageToken") == "" {
						fmt.Fprint(w, `{"files":[],"nextPageToken":"next"}`)
					} else {
						fmt.Fprint(w, `{"files":[{"id":"zip","mimeType":"application/zip"}]}`)
					}
					return
				}
				if r.URL.Query().Get("pageToken") == "" {
					fmt.Fprint(w, `{"items":[],"nextPageToken":"next"}`)
				} else {
					fmt.Fprint(w, `{"items":[{"id":"zip","mimeType":"application/zip","etag":"tag"}]}`)
				}
			}))
			defer server.Close()
			api := backendForTest(t, server, backend)
			if err := api.Patch(context.Background(), "root", "", File{Title: "no"}); err == nil || calls != 0 {
				t.Fatal("unconditional patch")
			}
			if err := api.Patch(context.Background(), "root", "expected", File{Properties: []Property{{Key: "gdrive-manifest", Value: "id", Visibility: "PUBLIC"}}}); err != nil {
				t.Fatal(err)
			}
			f, err := api.FindArchive(context.Background(), "folder", "main.zip")
			wantCalls := 3
			if backend == "sdkv3" {
				wantCalls = 4
			}
			if err != nil || f.ID != "zip" || calls != wantCalls {
				t.Fatal(f, calls, err)
			}
		})
	}
}

func TestSDKSessionGuard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://untrusted.invalid/session-secret")
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	client := &http.Client{Transport: sessionGuard{http.DefaultTransport}}
	res, err := client.Post(server.URL+"?uploadType=resumable", "application/json", strings.NewReader(`{}`))
	if res != nil {
		res.Body.Close()
	}
	if err == nil {
		t.Fatal("accepted external upload session")
	}
}

func TestAPICancelAndRetryLimit(t *testing.T) {
	for _, backend := range []string{"http", "sdkv2", "sdkv3"} {
		t.Run(backend, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(429)
				io.WriteString(w, `{"error":{"code":429}}`)
			}))
			defer server.Close()
			api := backendForTest(t, server, backend)
			if _, err := api.Metadata(context.Background(), "root"); err == nil || calls != 6 {
				t.Fatal("unbounded retry", calls, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := api.Metadata(ctx, "root"); err == nil {
				t.Fatal("ignored cancellation")
			}
		})
	}
}
