package drive

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		retry  bool
	}{
		{"429", 429, `{}`, true}, {"503", 503, `{}`, true},
		{"user quota", 403, `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`, true},
		{"rate quota", 403, `{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`, true},
		{"permission", 403, `{"error":{"errors":[{"reason":"insufficientFilePermissions"}]}}`, false},
		{"storage quota", 403, `{"error":{"errors":[{"reason":"storageQuotaExceeded"}]}}`, false},
		{"conflict", 412, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, waits := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.Header().Set("Retry-After", "3")
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
					return
				}
				io.WriteString(w, "ok")
			}))
			defer server.Close()
			client := NewClient(server.Client())
			client.sleep = func(ctx context.Context, d time.Duration) error {
				waits++
				if d < 3*time.Second {
					t.Error("Retry-After ignored")
				}
				return nil
			}
			res, err := client.request(context.Background(), "GET", server.URL, nil, nil)
			if res != nil {
				res.Body.Close()
			}
			if tc.retry {
				if err != nil || calls != 2 || waits != 1 {
					t.Fatalf("retry failed: %d calls, %d waits, %v", calls, waits, err)
				}
			} else if err == nil || calls != 1 || waits != 0 {
				t.Fatal("retried a permanent error")
			}
		})
	}
}

func TestRetryLimitAndCancellation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(429) }))
	defer server.Close()
	client := NewClient(server.Client())
	client.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := client.request(context.Background(), "GET", server.URL, nil, nil); err == nil || calls != 6 {
		t.Fatalf("retry limit: %d, %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.request(ctx, "GET", server.URL, nil, nil); err == nil {
		t.Fatal("ignored canceled context")
	}
}

func TestUploadOffsets(t *testing.T) {
	for _, raw := range []string{"bytes=1-20", "bytes=0--1", "bytes=0-100", "bytes=0-nope"} {
		if _, err := uploadOffset(raw, 100); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if offset, err := uploadOffset("bytes=0-49", 100); err != nil || offset != 50 {
		t.Fatal(offset, err)
	}
}
