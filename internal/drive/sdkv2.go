package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	sdk "google.golang.org/api/drive/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// SDKV2Client uses the generated v2 client for every API operation, including
// native Media uploads. Service.BasePath may be set by isolated HTTP tests only.
type SDKV2Client struct {
	Service *sdk.Service
	retry   *Client // Shared backoff policy only; never performs SDK API requests.
}

var _ API = (*SDKV2Client)(nil)

func NewSDKV2Client(ctx context.Context, client *http.Client) (*SDKV2Client, error) {
	copyClient := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copyClient.Transport = sessionGuard{transport}
	service, err := sdk.NewService(ctx, option.WithHTTPClient(&copyClient), option.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return nil, errors.New("initialize Drive v2 SDK client failed")
	}
	return &SDKV2Client{Service: service, retry: NewClient(client)}, nil
}

func fromSDK(f *sdk.File) File {
	if f == nil {
		return File{}
	}
	result := File{ID: f.Id, Title: f.Title, MIME: f.MimeType, ETag: f.Etag}
	for _, p := range f.Parents {
		if p != nil {
			result.Parents = append(result.Parents, Parent{ID: p.Id})
		}
	}
	for _, p := range f.Properties {
		if p != nil {
			result.Properties = append(result.Properties, Property{Key: p.Key, Value: p.Value, Visibility: p.Visibility})
		}
	}
	if f.Labels != nil {
		result.Labels.Trashed = f.Labels.Trashed
	}
	return result
}

func toSDK(f File) *sdk.File {
	result := &sdk.File{Id: f.ID, Title: f.Title, MimeType: f.MIME}
	for _, p := range f.Parents {
		result.Parents = append(result.Parents, &sdk.ParentReference{Id: p.ID})
	}
	for _, p := range f.Properties {
		result.Properties = append(result.Properties, &sdk.Property{Key: p.Key, Value: p.Value, Visibility: p.Visibility})
	}
	return result
}

func (c *SDKV2Client) Metadata(ctx context.Context, id string) (File, error) {
	f, err := sdkCall(ctx, c.retry, func() (*sdk.File, error) {
		return c.Service.Files.Get(id).SupportsAllDrives(true).Fields("id,title,mimeType,etag,parents(id),properties(key,value,visibility),labels(trashed)").Context(ctx).Do()
	})
	if err != nil {
		return File{}, err
	}
	result := fromSDK(f)
	if result.Labels.Trashed {
		return File{}, errors.New("repository file is trashed")
	}
	return result, nil
}

func (c *SDKV2Client) generateID(ctx context.Context) (string, error) {
	ids, err := sdkCall(ctx, c.retry, func() (*sdk.GeneratedIds, error) {
		return c.Service.Files.GenerateIds().MaxResults(1).Space("drive").Context(ctx).Do()
	})
	if err != nil {
		return "", err
	}
	if ids == nil || len(ids.Ids) != 1 {
		return "", errors.New("Drive did not generate a file ID")
	}
	return ids.Ids[0], nil
}

func (c *SDKV2Client) CreateFolder(ctx context.Context, root, name string) (string, error) {
	id, err := c.generateID(ctx)
	if err != nil {
		return "", err
	}
	f := File{ID: id, Title: name, MIME: folderMIME, Parents: []Parent{{ID: root}}, Properties: []Property{{Key: "gdrive-format", Value: "1", Visibility: "PUBLIC"}}}
	_, err = sdkCall(ctx, c.retry, func() (*sdk.File, error) {
		return c.Service.Files.Insert(toSDK(f)).SupportsAllDrives(true).Context(ctx).Do()
	})
	if err != nil && !statusIs(err, 409) {
		return "", err
	}
	actual, err := c.Metadata(ctx, id)
	if err != nil {
		return "", err
	}
	if !actual.in(root) || actual.Title != name || actual.MIME != folderMIME || actual.value("gdrive-format") != "1" {
		return "", errors.New("created repository directory is invalid")
	}
	return id, nil
}

func (c *SDKV2Client) FindArchive(ctx context.Context, dir, name string) (File, error) {
	escape := func(v string) string { return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) }
	q := fmt.Sprintf("'%s' in parents and title = '%s' and trashed = false", escape(dir), escape(name))
	var match File
	pageToken := ""
	for {
		page, err := sdkCall(ctx, c.retry, func() (*sdk.FileList, error) {
			return c.Service.Files.List().Q(q).SupportsAllDrives(true).IncludeItemsFromAllDrives(true).MaxResults(1000).PageToken(pageToken).Fields("items(id,mimeType,etag),nextPageToken,incompleteSearch").Context(ctx).Do()
		})
		if err != nil {
			return File{}, err
		}
		if page == nil {
			return File{}, errors.New("Drive returned an invalid archive list")
		}
		if page.IncompleteSearch {
			return File{}, errors.New("Drive archive search was incomplete; retry push")
		}
		for _, f := range page.Items {
			item := fromSDK(f)
			if match.ID != "" {
				return File{}, errors.New("multiple Drive files have the ZIP name; remove the duplicate before pushing")
			}
			if item.MIME != "application/zip" && item.MIME != "application/octet-stream" {
				return File{}, errors.New("ZIP name is occupied by a non-archive Drive file")
			}
			if item.ID == "" || item.ETag == "" {
				return File{}, errors.New("Drive returned incomplete ZIP metadata")
			}
			match = item
		}
		if page.NextPageToken == "" {
			return match, nil
		}
		pageToken = page.NextPageToken
	}
}

func (c *SDKV2Client) Patch(ctx context.Context, id, etag string, patch File) error {
	if etag == "" {
		return errors.New("missing ETag; refusing an unconditional update")
	}
	_, err := sdkCall(ctx, c.retry, func() (*sdk.File, error) {
		call := c.Service.Files.Patch(id, toSDK(patch)).SupportsAllDrives(true).Context(ctx)
		call.Header().Set("If-Match", etag)
		return call.Do()
	})
	return err
}

func (c *SDKV2Client) Download(ctx context.Context, id string, out io.Writer, limit int64) error {
	res, err := sdkCall(ctx, c.retry, func() (*http.Response, error) {
		return c.Service.Files.Get(id).SupportsAllDrives(true).Context(ctx).Download()
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, err = io.Copy(out, io.LimitReader(res.Body, limit))
	return sdkError(ctx, err)
}

func (c *SDKV2Client) UploadFile(ctx context.Context, existing File, dir, name string, reader io.ReadSeeker, size int64) (string, error) {
	return sdkUploadFile(ctx, c, c.retry, existing, dir, name, reader, size)
}

func (c *SDKV2Client) upload(ctx context.Context, existing, meta File, input io.Reader, options []googleapi.MediaOption, update func(int64, int64)) (string, error) {
	var f *sdk.File
	var err error
	if existing.ID == "" {
		f, err = c.Service.Files.Insert(toSDK(meta)).SupportsAllDrives(true).Context(ctx).Media(input, options...).ProgressUpdater(update).Do()
	} else {
		call := c.Service.Files.Update(existing.ID, toSDK(meta)).SupportsAllDrives(true).Context(ctx).Media(input, options...).ProgressUpdater(update)
		call.Header().Set("If-Match", existing.ETag)
		f, err = call.Do()
	}
	if f == nil {
		return "", err
	}
	return f.Id, err
}
