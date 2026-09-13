// Package drive stores immutable packs/manifests in Google Drive and publishes
// their IDs using conditional metadata updates on the user-selected folder.
package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jr-dragon/git-remote-gdrive/internal/progress"
)

const folderMIME = "application/vnd.google-apps.folder"
const chunkSize int64 = 8 << 20

type Property struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Visibility string `json:"visibility"`
}
type Parent struct {
	ID string `json:"id"`
}
type File struct {
	ID         string     `json:"id,omitempty"`
	Title      string     `json:"title,omitempty"`
	MIME       string     `json:"mimeType,omitempty"`
	ETag       string     `json:"etag,omitempty"`
	Parents    []Parent   `json:"parents,omitempty"`
	Properties []Property `json:"properties,omitempty"`
	Labels     struct {
		Trashed bool `json:"trashed"`
	} `json:"labels,omitempty"`
}

// Internal aliases keep the original HTTP codec independent of the SDK types.
type file = File
type property = Property
type parent = Parent

func (f File) value(key string) string {
	for _, p := range f.Properties {
		if p.Key == key && p.Visibility == "PUBLIC" {
			return p.Value
		}
	}
	return ""
}
func (f File) in(parentID string) bool {
	for _, p := range f.Parents {
		if p.ID == parentID {
			return true
		}
	}
	return false
}

type apiError struct {
	Status int
	Reason string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Google Drive request failed (HTTP %d, %s)", e.Status, e.Reason)
}
func statusIs(err error, status int) bool {
	var e *apiError
	return errors.As(err, &e) && e.Status == status
}

// Client uses v2 metadata because it exposes file ETags for If-Match publishing.
// BaseURL is injectable for local HTTP tests; production uses googleapis.com.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	sleep   func(context.Context, time.Duration) error
}

func NewClient(httpClient *http.Client) *Client {
	return &Client{HTTP: httpClient, BaseURL: "https://www.googleapis.com", sleep: sleepContext}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryable(status int, body []byte) bool {
	if status == 429 || status == 500 || status == 502 || status == 503 || status == 504 {
		return true
	}
	if status != 403 {
		return false
	}
	var payload struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	for _, e := range payload.Error.Errors {
		if e.Reason == "rateLimitExceeded" || e.Reason == "userRateLimitExceeded" {
			return true
		}
	}
	return false
}

func (c *Client) wait(ctx context.Context, attempt int, retryAfter string) error {
	delay := time.Second*time.Duration(1<<min(attempt, 5)) + time.Duration(rand.Int64N(int64(time.Second)))
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		delay = max(delay, time.Duration(min(seconds, 300))*time.Second)
	} else if date, err := http.ParseTime(retryAfter); err == nil {
		delay = max(delay, min(time.Until(date), 5*time.Minute))
	}
	progress.Step(ctx, fmt.Sprintf("Drive request interrupted or rate limited; retrying in %s (retry %d)", delay.Round(time.Second), attempt+1))
	return c.sleep(ctx, delay)
}

// request retries replayable requests with bounded exponential backoff. All
// mutations use generated IDs or If-Match, so a retry cannot duplicate a commit.
func (c *Client) request(ctx context.Context, method, endpoint string, data []byte, headers http.Header) (*http.Response, error) {
	for attempt := 0; attempt < 6; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if headers != nil {
			req.Header = headers.Clone()
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == 5 {
				return nil, errors.New("Google Drive connection failed after retries")
			}
			if err := c.wait(ctx, attempt, ""); err != nil {
				return nil, err
			}
			continue
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 || res.StatusCode == 308 {
			return res, nil
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		if retryable(res.StatusCode, body) && attempt < 5 {
			if err := c.wait(ctx, attempt, res.Header.Get("Retry-After")); err != nil {
				return nil, err
			}
			continue
		}
		// Never include raw response bodies: OAuth and upload URLs may be sensitive.
		return nil, &apiError{Status: res.StatusCode, Reason: http.StatusText(res.StatusCode)}
	}
	return nil, errors.New("Google Drive retry limit reached")
}

func (c *Client) metadata(ctx context.Context, id string) (file, error) {
	u := c.BaseURL + "/drive/v2/files/" + url.PathEscape(id) + "?supportsAllDrives=true&fields=id,title,mimeType,etag,parents(id),properties(key,value,visibility),labels(trashed)"
	res, err := c.request(ctx, "GET", u, nil, nil)
	if err != nil {
		return file{}, err
	}
	defer res.Body.Close()
	var f file
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&f)
	if err == nil && f.Labels.Trashed {
		err = errors.New("repository file is trashed")
	}
	return f, err
}

func (c *Client) generateID(ctx context.Context) (string, error) {
	res, err := c.request(ctx, "GET", c.BaseURL+"/drive/v2/files/generateIds?maxResults=1&space=drive", nil, nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var result struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.IDs) != 1 {
		return "", errors.New("Drive did not generate a file ID")
	}
	return result.IDs[0], nil
}

func (c *Client) createNamedFolder(ctx context.Context, root, name string) (string, error) {
	id, err := c.generateID(ctx)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(file{ID: id, Title: name, MIME: folderMIME, Parents: []parent{{ID: root}}, Properties: []property{{Key: "gdrive-format", Value: "1", Visibility: "PUBLIC"}}})
	res, err := c.request(ctx, "POST", c.BaseURL+"/drive/v2/files?supportsAllDrives=true", data, http.Header{"Content-Type": {"application/json"}})
	if err != nil && !statusIs(err, 409) {
		return "", err
	}
	if res != nil {
		res.Body.Close()
	}
	f, err := c.metadata(ctx, id)
	if err != nil {
		return "", err
	}
	if !f.in(root) || f.Title != name || f.MIME != folderMIME || f.value("gdrive-format") != "1" {
		return "", errors.New("created repository directory is invalid")
	}
	return id, nil
}

// findArchive looks only inside the canonical archive directory. A name search
// also reuses ZIPs left by deleted refs or attempts that did not publish a manifest.
func (c *Client) findArchive(ctx context.Context, dir, name string) (file, error) {
	escape := func(value string) string { return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) }
	query := url.Values{
		"q":                 {fmt.Sprintf("'%s' in parents and title = '%s' and trashed = false", escape(dir), escape(name))},
		"supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"},
		"maxResults": {"1000"}, "fields": {"items(id,mimeType,etag),nextPageToken,incompleteSearch"},
	}
	var match file
	for {
		res, err := c.request(ctx, "GET", c.BaseURL+"/drive/v2/files?"+query.Encode(), nil, nil)
		if err != nil {
			return file{}, err
		}
		var page struct {
			Items      []file `json:"items"`
			Next       string `json:"nextPageToken"`
			Incomplete bool   `json:"incompleteSearch"`
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&page)
		res.Body.Close()
		if err != nil {
			return file{}, err
		}
		if page.Incomplete {
			return file{}, errors.New("Drive archive search was incomplete; retry push")
		}
		for _, item := range page.Items {
			if match.ID != "" {
				return file{}, errors.New("multiple Drive files have the ZIP name; remove the duplicate before pushing")
			}
			if item.MIME != "application/zip" && item.MIME != "application/octet-stream" {
				return file{}, errors.New("ZIP name is occupied by a non-archive Drive file")
			}
			if item.ID == "" || item.ETag == "" {
				return file{}, errors.New("Drive returned incomplete ZIP metadata")
			}
			match = item
		}
		if page.Next == "" {
			return match, nil
		}
		query.Set("pageToken", page.Next)
	}
}

func uploadLabel(name string) string {
	label := "Uploading " + name
	if strings.HasSuffix(name, ".pack") {
		label = "Uploading Git pack"
	}
	if strings.HasPrefix(name, "asset-sha256-") {
		digest := strings.TrimPrefix(name, "asset-sha256-")
		label = "Uploading asset " + digest[:min(len(digest), 12)]
	}
	if strings.HasSuffix(name, ".zip") {
		label = "Uploading ZIP " + name
	}
	return label
}

func (c *Client) uploadFile(ctx context.Context, existing file, dir, name string, reader io.ReadSeeker, size int64) (uploadedID string, err error) {
	transfer := progress.Start(ctx, uploadLabel(name), size)
	defer func() { transfer.Finish(err) }()
	id := existing.ID
	method, endpoint := "POST", c.BaseURL+"/upload/drive/v2/files?uploadType=resumable&supportsAllDrives=true"
	metadata := file{Title: name}
	if id == "" {
		var err error
		id, err = c.generateID(ctx)
		if err != nil {
			return "", err
		}
		metadata.ID, metadata.Parents = id, []parent{{ID: dir}}
	} else {
		if existing.ETag == "" {
			return "", errors.New("Drive returned no ZIP ETag; refusing an unconditional overwrite")
		}
		method, endpoint = "PUT", c.BaseURL+"/upload/drive/v2/files/"+url.PathEscape(id)+"?uploadType=resumable&supportsAllDrives=true"
	}
	mimeType := "application/octet-stream"
	if strings.HasSuffix(name, ".zip") {
		mimeType = "application/zip"
	}
	metadata.MIME = mimeType
	data, _ := json.Marshal(metadata)
	headers := http.Header{"Content-Type": {"application/json"}, "X-Upload-Content-Type": {mimeType}, "X-Upload-Content-Length": {strconv.FormatInt(size, 10)}}
	if existing.ID != "" {
		headers.Set("If-Match", existing.ETag)
	}
	res, err := c.request(ctx, method, endpoint, data, headers)
	if err != nil {
		return "", err
	}
	session := res.Header.Get("Location")
	res.Body.Close()
	u, err := url.Parse(session)
	base, _ := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil {
		return "", errors.New("Drive returned an invalid upload session URL")
	}
	if size == 0 {
		response, err := c.request(ctx, "PUT", session, nil, http.Header{"Content-Type": {mimeType}, "Content-Range": {"bytes */0"}})
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 201 {
			return "", errors.New("empty upload ended without a completion response")
		}
		return id, nil
	}
	var offset int64
	stalled := 0
	for offset < size {
		end := min(offset+chunkSize, size)
		if _, err := reader.Seek(offset, io.SeekStart); err != nil {
			return "", err
		}
		// A transport may still be closing a request after a network error. Give
		// each attempt its own bounded buffer so probing/resuming cannot race a
		// previous request's reads against the source file's seek position.
		chunk := make([]byte, end-offset)
		if _, err := io.ReadFull(reader, chunk); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, "PUT", session, transfer.Reader(bytes.NewReader(chunk), offset))
		if err != nil {
			return "", err
		}
		req.ContentLength = end - offset
		req.Header.Set("Content-Type", mimeType)
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end-1, size))
		response, sendErr := c.HTTP.Do(req)
		if sendErr == nil && (response.StatusCode == 200 || response.StatusCode == 201) {
			response.Body.Close()
			return id, nil
		}
		probe := sendErr != nil
		var retryAfter string
		if response != nil {
			if response.StatusCode == 308 {
				next, err := uploadOffset(response.Header.Get("Range"), size)
				response.Body.Close()
				if err != nil {
					return "", err
				}
				if next > offset && next <= end {
					offset = next
					transfer.Update(offset)
					stalled = 0
					continue
				}
				probe = true
			} else {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
				response.Body.Close()
				retryAfter = response.Header.Get("Retry-After")
				if !retryable(response.StatusCode, body) {
					return "", &apiError{Status: response.StatusCode, Reason: "resumable upload failed; retry push"}
				}
				probe = true
			}
		}
		if probe {
			if stalled >= 5 {
				return "", errors.New("resumable upload made no progress; retry push")
			}
			if err := c.wait(ctx, stalled, retryAfter); err != nil {
				return "", err
			}
			stalled++
			status, err := c.request(ctx, "PUT", session, nil, http.Header{"Content-Range": {fmt.Sprintf("bytes */%d", size)}})
			if err != nil {
				return "", err
			}
			if status.StatusCode == 200 || status.StatusCode == 201 {
				status.Body.Close()
				return id, nil
			}
			next, err := uploadOffset(status.Header.Get("Range"), size)
			status.Body.Close()
			if err != nil {
				return "", err
			}
			if next < offset || next > end {
				return "", errors.New("invalid resumable upload progress")
			}
			if next > offset {
				stalled = 0
			}
			offset = next
			transfer.Update(offset)
		}
	}
	return "", errors.New("upload ended without a completion response")
}

func uploadOffset(value string, size int64) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if !strings.HasPrefix(value, "bytes=0-") {
		return 0, errors.New("invalid upload Range header")
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(value, "bytes=0-"), 10, 64)
	if err != nil || n < 0 || n >= size {
		return 0, errors.New("invalid upload Range offset")
	}
	return n + 1, nil
}
