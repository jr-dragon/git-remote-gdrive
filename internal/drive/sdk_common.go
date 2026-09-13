package drive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jr-dragon/git-remote-gdrive/internal/progress"
	"google.golang.org/api/googleapi"
)

type sdkOperations interface {
	API
	generateID(context.Context) (string, error)
	upload(context.Context, File, File, io.Reader, []googleapi.MediaOption, func(int64, int64)) (string, error)
}

// Preserve the HTTP backend's same-origin session constraint before the SDK
// follows a resumable Location with the authenticated client.
type sessionGuard struct{ base http.RoundTripper }

func (g sessionGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := g.base.RoundTrip(req)
	if err != nil {
		return res, err
	}
	if req.URL.Query().Get("uploadType") == "resumable" && res.StatusCode >= 200 && res.StatusCode < 300 {
		u, err := url.Parse(res.Header.Get("Location"))
		if err != nil || u.Scheme != req.URL.Scheme || u.Host != req.URL.Host || u.User != nil {
			res.Body.Close()
			return nil, errors.New("Drive returned an invalid upload session URL")
		}
	}
	return res, nil
}

func sdkError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var api *googleapi.Error
	if errors.As(err, &api) {
		return &apiError{Status: api.Code, Reason: http.StatusText(api.Code)}
	}
	// SDK errors may contain request URLs, upload sessions or raw response bodies.
	return errors.New("Drive SDK operation failed; fetch to verify remote state before retry")
}

// Metadata and whole-operation retries are bounded and share the HTTP backend's
// quota classification. Native resumable chunk retries are owned by the SDK.
func sdkCall[T any](ctx context.Context, c *Client, call func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; attempt < 6; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := call()
		if err == nil {
			return result, nil
		}
		var api *googleapi.Error
		retry, after := false, ""
		if errors.As(err, &api) {
			retry = retryable(api.Code, nil)
			if api.Code == 403 {
				for _, item := range api.Errors {
					if item.Reason == "rateLimitExceeded" || item.Reason == "userRateLimitExceeded" {
						retry = true
					}
				}
			}
			after = api.Header.Get("Retry-After")
		} else {
			var network interface{ Timeout() bool }
			retry = errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
		}
		if !retry || attempt == 5 {
			return zero, sdkError(ctx, err)
		}
		if err := c.wait(ctx, attempt, after); err != nil {
			return zero, err
		}
	}
	return zero, errors.New("Drive SDK retry limit reached")
}

func sdkUploadFile(ctx context.Context, c sdkOperations, retry *Client, existing File, dir, name string, reader io.ReadSeeker, size int64) (id string, err error) {
	transfer := progress.Start(ctx, uploadLabel(name), size)
	defer func() { transfer.Finish(err) }()
	if size < 0 || size == 1<<63-1 {
		return "", errors.New("invalid upload size")
	}
	actual, err := reader.Seek(0, io.SeekEnd)
	if err != nil {
		return "", err
	}
	if actual != size {
		return "", errors.New("upload source size mismatch")
	}
	id = existing.ID
	meta := File{Title: name, MIME: "application/octet-stream"}
	if strings.HasSuffix(name, ".zip") {
		meta.MIME = "application/zip"
	}
	if id == "" {
		id, err = c.generateID(ctx)
		if err != nil {
			return "", err
		}
		meta.ID, meta.Parents = id, []Parent{{ID: dir}}
	} else if existing.ETag == "" {
		return "", errors.New("Drive returned no ZIP ETag; refusing an unconditional overwrite")
	}
	f, err := sdkCall(ctx, retry, func() (string, error) {
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		bufferSize := int(chunkSize)
		var input io.Reader = reader
		if size < chunkSize {
			// The size is known: avoid allocating an 8 MiB SDK buffer merely to
			// discover a small file. Isolate each retry's reader because the
			// SDK's multipart goroutine may still unwind after a failed request.
			bufferSize = 0
			data := make([]byte, size)
			if _, err := io.ReadFull(reader, data); err != nil {
				return "", err
			}
			input = transfer.Reader(bytes.NewReader(data), 0)
		}
		options := []googleapi.MediaOption{googleapi.ContentType(meta.MIME), googleapi.ChunkSize(bufferSize), googleapi.ChunkRetryDeadline(32 * time.Second)}
		update := func(current, total int64) { transfer.Update(current) }
		return c.upload(ctx, existing, meta, input, options, update)
	})
	if err != nil {
		// A retried generated-ID insert can return 409 after a lost success
		// response; an update can return 412. Verify exact bytes before success.
		if ctx.Err() == nil && matchesSDKUpload(ctx, c, id, dir, name, meta.MIME, reader, size) {
			return id, nil
		}
		return "", err
	}
	if f != id {
		return "", errors.New("Drive returned an unexpected uploaded file ID")
	}
	return id, nil
}

func matchesSDKUpload(ctx context.Context, c API, id, dir, name, mime string, reader io.ReadSeeker, size int64) bool {
	f, err := c.Metadata(ctx, id)
	if err != nil || !f.in(dir) || f.Title != name || f.MIME != mime {
		return false
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return false
	}
	want, got := sha256.New(), sha256.New()
	if n, err := io.Copy(want, reader); err != nil || n != size {
		return false
	}
	count := &sizeWriter{out: got}
	if err := c.Download(ctx, id, count, size+1); err != nil || count.n != size {
		return false
	}
	return string(want.Sum(nil)) == string(got.Sum(nil))
}

type sizeWriter struct {
	out io.Writer
	n   int64
}

func (w *sizeWriter) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	w.n += int64(n)
	return n, err
}
