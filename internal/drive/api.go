package drive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
)

// API isolates transport choices from repository identity, validation and CAS.
// Implementations must not publish refs as a side effect of an upload.
type API interface {
	Metadata(context.Context, string) (File, error)
	CreateFolder(context.Context, string, string) (string, error)
	FindArchive(context.Context, string, string) (File, error)
	UploadFile(context.Context, File, string, string, io.ReadSeeker, int64) (string, error)
	Download(context.Context, string, io.Writer, int64) error
	Patch(context.Context, string, string, File) error
}

// NewAPI chooses a transport explicitly. Empty selects the official v3 SDK
// implementation. No endpoint or credential settings are taken from the backend.
func NewAPI(ctx context.Context, client *http.Client, backend string) (API, error) {
	switch backend {
	case "http":
		return NewClient(client), nil
	case "sdkv2":
		return NewSDKV2Client(ctx, client)
	case "", "sdkv3":
		return NewSDKV3Client(ctx, client)
	default:
		return nil, errors.New("GIT_GDRIVE_API_BACKEND must be http, sdkv2, or sdkv3")
	}
}

func ConfiguredAPI(ctx context.Context, client *http.Client) (API, error) {
	return NewAPI(ctx, client, os.Getenv("GIT_GDRIVE_API_BACKEND"))
}

var _ API = (*Client)(nil)

func (c *Client) Metadata(ctx context.Context, id string) (File, error) { return c.metadata(ctx, id) }
func (c *Client) CreateFolder(ctx context.Context, root, name string) (string, error) {
	return c.createNamedFolder(ctx, root, name)
}
func (c *Client) FindArchive(ctx context.Context, dir, name string) (File, error) {
	return c.findArchive(ctx, dir, name)
}
func (c *Client) UploadFile(ctx context.Context, existing File, dir, name string, reader io.ReadSeeker, size int64) (string, error) {
	return c.uploadFile(ctx, existing, dir, name, reader, size)
}

func (c *Client) Download(ctx context.Context, id string, out io.Writer, limit int64) error {
	res, err := c.request(ctx, "GET", c.BaseURL+"/drive/v2/files/"+url.PathEscape(id)+"?alt=media&supportsAllDrives=true", nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, err = io.Copy(out, io.LimitReader(res.Body, limit))
	return err
}

func (c *Client) Patch(ctx context.Context, id, etag string, patch File) error {
	if etag == "" {
		return errors.New("missing ETag; refusing an unconditional update")
	}
	data, err := json.Marshal(struct {
		Title      string     `json:"title,omitempty"`
		Properties []Property `json:"properties,omitempty"`
	}{patch.Title, patch.Properties})
	if err != nil {
		return err
	}
	res, err := c.request(ctx, "PATCH", c.BaseURL+"/drive/v2/files/"+url.PathEscape(id)+"?supportsAllDrives=true", data, http.Header{"Content-Type": {"application/json"}, "If-Match": {etag}})
	if res != nil {
		res.Body.Close()
	}
	return err
}
