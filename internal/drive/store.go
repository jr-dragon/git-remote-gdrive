package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/jr-dragon/git-remote-gdrive/internal/repository"
)

const directoryKey = "gdrive-repo"
const manifestKey = "gdrive-manifest"

type Store struct {
	Client    *Client
	Root      string
	directory string
}

func (s *Store) root(ctx context.Context) (file, error) {
	if !repository.ValidID(s.Root) {
		return file{}, errors.New("invalid Google Drive folder ID")
	}
	f, err := s.Client.metadata(ctx, s.Root)
	if err != nil {
		return f, err
	}
	if f.MIME != folderMIME {
		return f, errors.New("gdrive URL must identify a Google Drive folder")
	}
	return f, nil
}

func (s *Store) Load(ctx context.Context) (*repository.Manifest, string, error) {
	root, err := s.root(ctx)
	if err != nil {
		return nil, "", err
	}
	dir, version := root.value(directoryKey), root.value(manifestKey)
	if dir == "" && version == "" {
		s.directory = ""
		return repository.Empty(), "", nil
	}
	if !repository.ValidID(dir) || !repository.ValidID(version) {
		return nil, "", errors.New("incomplete repository pointer on Drive folder")
	}
	folder, err := s.Client.metadata(ctx, dir)
	if err != nil {
		return nil, "", err
	}
	if folder.MIME != folderMIME || !folder.in(s.Root) || folder.value("gdrive-format") != "1" {
		return nil, "", errors.New("invalid repository directory")
	}
	s.directory = dir
	var content bytes.Buffer
	if err := s.download(ctx, version, &content, repository.MaxManifestSize+1); err != nil {
		return nil, "", err
	}
	if content.Len() > repository.MaxManifestSize {
		return nil, "", errors.New("repository manifest exceeds size limit")
	}
	var m repository.Manifest
	if err := json.Unmarshal(content.Bytes(), &m); err != nil {
		return nil, "", errors.New("invalid repository manifest JSON")
	}
	if m.Root != s.Root || m.Directory != dir {
		return nil, "", errors.New("manifest belongs to a different repository")
	}
	if err := m.Validate(); err != nil {
		return nil, "", err
	}
	return &m, version, nil
}

func (s *Store) ensureDirectory(ctx context.Context) error {
	if s.directory != "" {
		return nil
	}
	dir, err := s.Client.createFolder(ctx, s.Root)
	if err != nil {
		return err
	}
	s.directory = dir
	return nil
}

func (s *Store) Upload(ctx context.Context, name string, reader io.ReadSeeker, size int64) (string, error) {
	if err := s.ensureDirectory(ctx); err != nil {
		return "", err
	}
	return s.Client.upload(ctx, s.directory, name, reader, size)
}

func (s *Store) download(ctx context.Context, id string, w io.Writer, limit int64) error {
	meta, err := s.Client.metadata(ctx, id)
	if err != nil {
		return err
	}
	if !meta.in(s.directory) || meta.MIME == folderMIME {
		return errors.New("repository file is outside its storage directory")
	}
	res, err := s.Client.request(ctx, "GET", s.Client.BaseURL+"/drive/v2/files/"+url.PathEscape(id)+"?alt=media&supportsAllDrives=true", nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, err = io.Copy(w, io.LimitReader(res.Body, limit))
	return err
}

func (s *Store) Download(ctx context.Context, pack repository.Pack, w io.Writer) error {
	if pack.Size < 0 || pack.Size == 1<<63-1 {
		return errors.New("invalid pack size")
	}
	return s.download(ctx, pack.ID, w, pack.Size+1)
}

func (s *Store) Publish(ctx context.Context, expected string, m *repository.Manifest) error {
	if err := s.ensureDirectory(ctx); err != nil {
		return err
	}
	m.Root, m.Directory = s.Root, s.directory
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(data) > repository.MaxManifestSize {
		return errors.New("repository manifest exceeds size limit")
	}
	id, err := s.Upload(ctx, "manifest.json", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	// Read again after uploads: our own child creation may change folder metadata.
	// Only the expected published manifest may be replaced, even with force push.
	root, err := s.root(ctx)
	if err != nil {
		return err
	}
	if root.value(manifestKey) != expected || (expected != "" && root.value(directoryKey) != s.directory) || (expected == "" && root.value(directoryKey) != "") {
		return repository.ErrConflict
	}
	if root.ETag == "" {
		return errors.New("Drive returned no ETag; refusing an unconditional repository update")
	}
	properties := make([]property, 0, len(root.Properties)+2)
	for _, p := range root.Properties {
		if p.Visibility != "PUBLIC" || (p.Key != directoryKey && p.Key != manifestKey) {
			properties = append(properties, p)
		}
	}
	properties = append(properties, property{directoryKey, s.directory, "PUBLIC"}, property{manifestKey, id, "PUBLIC"})
	patch, _ := json.Marshal(struct {
		Properties []property `json:"properties"`
	}{properties})
	res, err := s.Client.request(ctx, "PATCH", s.Client.BaseURL+"/drive/v2/files/"+url.PathEscape(s.Root)+"?supportsAllDrives=true", patch, http.Header{"Content-Type": {"application/json"}, "If-Match": {root.ETag}})
	if res != nil {
		res.Body.Close()
	}
	if err != nil {
		// A lost success response followed by 412 is still our successful commit.
		current, readErr := s.root(ctx)
		if readErr == nil && current.value(manifestKey) == id && current.value(directoryKey) == s.directory {
			return nil
		}
		if statusIs(err, 412) {
			return repository.ErrConflict
		}
		return fmt.Errorf("publish repository (fetch to verify remote state before retry): %w", err)
	}
	return nil
}
