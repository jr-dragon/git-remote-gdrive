package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	sdk "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// SDKV3Client uses the official v3 SDK, including media uploads. Tests may
// override Service.BasePath; production endpoints are owned by the SDK.
type SDKV3Client struct {
	Service *sdk.Service
	retry   *Client
}

var _ API = (*SDKV3Client)(nil)

func NewSDKV3Client(ctx context.Context, client *http.Client) (*SDKV3Client, error) {
	copyClient := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copyClient.Transport = sessionGuard{transport}
	service, err := sdk.NewService(ctx, option.WithHTTPClient(&copyClient), option.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return nil, errors.New("initialize Drive v3 SDK client failed")
	}
	return &SDKV3Client{Service: service, retry: NewClient(client)}, nil
}

func fromSDKV3(f *sdk.File) File {
	if f == nil {
		return File{}
	}
	result := File{ID: f.Id, Title: f.Name, MIME: f.MimeType, ETag: f.Header.Get("ETag")}
	result.Labels.Trashed = f.Trashed
	for _, id := range f.Parents {
		result.Parents = append(result.Parents, Parent{ID: id})
	}
	for key, value := range f.Properties {
		result.Properties = append(result.Properties, Property{Key: key, Value: value, Visibility: "PUBLIC"})
	}
	for key, value := range f.AppProperties {
		result.Properties = append(result.Properties, Property{Key: key, Value: value, Visibility: "PRIVATE"})
	}
	return result
}

func toSDKV3(f File) *sdk.File {
	result := &sdk.File{Id: f.ID, Name: f.Title, MimeType: f.MIME}
	for _, p := range f.Parents {
		result.Parents = append(result.Parents, p.ID)
	}
	for _, p := range f.Properties {
		if p.Visibility == "PUBLIC" {
			if result.Properties == nil {
				result.Properties = make(map[string]string)
			}
			result.Properties[p.Key] = p.Value
		} else {
			if result.AppProperties == nil {
				result.AppProperties = make(map[string]string)
			}
			result.AppProperties[p.Key] = p.Value
		}
	}
	return result
}

func (c *SDKV3Client) Metadata(ctx context.Context, id string) (File, error) {
	f, err := sdkCall(ctx, c.retry, func() (*sdk.File, error) {
		return c.Service.Files.Get(id).SupportsAllDrives(true).Fields("id,name,mimeType,parents,properties,appProperties,trashed").Context(ctx).Do()
	})
	if err != nil {
		return File{}, err
	}
	result := fromSDKV3(f)
	if result.Labels.Trashed {
		return File{}, errors.New("repository file is trashed")
	}
	// v3 has no JSON etag field. Only a file GET's response header can supply
	// the precondition; Store and ZIP writes reject a missing ETag.
	return result, nil
}

func (c *SDKV3Client) generateID(ctx context.Context) (string, error) {
	ids, err := sdkCall(ctx, c.retry, func() (*sdk.GeneratedIds, error) {
		return c.Service.Files.GenerateIds().Count(1).Space("drive").Context(ctx).Do()
	})
	if err != nil {
		return "", err
	}
	if ids == nil || len(ids.Ids) != 1 {
		return "", errors.New("Drive did not generate a file ID")
	}
	return ids.Ids[0], nil
}

func (c *SDKV3Client) CreateFolder(ctx context.Context, root, name string) (string, error) {
	id, err := c.generateID(ctx)
	if err != nil {
		return "", err
	}
	f := File{ID: id, Title: name, MIME: folderMIME, Parents: []Parent{{ID: root}}, Properties: []Property{{Key: "gdrive-format", Value: "1", Visibility: "PUBLIC"}}}
	_, err = sdkCall(ctx, c.retry, func() (*sdk.File, error) {
		return c.Service.Files.Create(toSDKV3(f)).SupportsAllDrives(true).Fields("id").Context(ctx).Do()
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

func (c *SDKV3Client) FindArchive(ctx context.Context, dir, name string) (File, error) {
	escape := func(v string) string { return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) }
	q := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false", escape(dir), escape(name))
	var match File
	pageToken := ""
	for {
		page, err := sdkCall(ctx, c.retry, func() (*sdk.FileList, error) {
			return c.Service.Files.List().Q(q).SupportsAllDrives(true).IncludeItemsFromAllDrives(true).PageSize(1000).PageToken(pageToken).Fields("files(id,mimeType),nextPageToken,incompleteSearch").Context(ctx).Do()
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
		for _, f := range page.Files {
			item := fromSDKV3(f)
			if match.ID != "" {
				return File{}, errors.New("multiple Drive files have the ZIP name; remove the duplicate before pushing")
			}
			if item.MIME != "application/zip" && item.MIME != "application/octet-stream" {
				return File{}, errors.New("ZIP name is occupied by a non-archive Drive file")
			}
			if item.ID == "" {
				return File{}, errors.New("Drive returned incomplete ZIP metadata")
			}
			match = item
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	if match.ID == "" {
		return match, nil
	}
	// A list response has no per-file ETags. Re-read the selected file and
	// validate its current identity before returning its conditional-write token.
	actual, err := c.Metadata(ctx, match.ID)
	if err != nil {
		return File{}, err
	}
	if actual.ID != match.ID || !actual.in(dir) || actual.Title != name || (actual.MIME != "application/zip" && actual.MIME != "application/octet-stream") || actual.ETag == "" {
		return File{}, errors.New("Drive returned incomplete or changed ZIP metadata")
	}
	return actual, nil
}

func (c *SDKV3Client) Patch(ctx context.Context, id, etag string, patch File) error {
	if etag == "" {
		return errors.New("missing ETag; refusing an unconditional update")
	}
	_, err := sdkCall(ctx, c.retry, func() (*sdk.File, error) {
		call := c.Service.Files.Update(id, toSDKV3(patch)).SupportsAllDrives(true).Fields("id").Context(ctx)
		call.Header().Set("If-Match", etag)
		return call.Do()
	})
	return err
}

func (c *SDKV3Client) Download(ctx context.Context, id string, out io.Writer, limit int64) error {
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

func (c *SDKV3Client) UploadFile(ctx context.Context, existing File, dir, name string, reader io.ReadSeeker, size int64) (string, error) {
	return sdkUploadFile(ctx, c, c.retry, existing, dir, name, reader, size)
}

func (c *SDKV3Client) upload(ctx context.Context, existing, meta File, input io.Reader, options []googleapi.MediaOption, update func(int64, int64)) (string, error) {
	var f *sdk.File
	var err error
	if existing.ID == "" {
		f, err = c.Service.Files.Create(toSDKV3(meta)).SupportsAllDrives(true).Fields("id").Context(ctx).Media(input, options...).ProgressUpdater(update).Do()
	} else {
		call := c.Service.Files.Update(existing.ID, toSDKV3(meta)).SupportsAllDrives(true).Fields("id").Context(ctx).Media(input, options...).ProgressUpdater(update)
		call.Header().Set("If-Match", existing.ETag)
		f, err = call.Do()
	}
	if f == nil {
		return "", err
	}
	return f.Id, err
}
