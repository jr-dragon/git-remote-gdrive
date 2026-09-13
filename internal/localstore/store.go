// Package localstore implements a portable repository without Google API metadata.
package localstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/jr-dragon/git-remote-gdrive/internal/progress"
	"github.com/jr-dragon/git-remote-gdrive/internal/repository"
)

const format = "git-remote-gdrive-local 1\n"
const lockName = ".gdrive-local.lock"
const directory = repository.DirectoryName

type Store struct {
	Path     string
	version  string
	archives map[string]string
}

var _ repository.Store = (*Store)(nil)

func New(path string) (*Store, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(p)
	if err != nil {
		return nil, fmt.Errorf("open local remote (create or download the root folder first): %w", err)
	}
	r.Close()
	return &Store{Path: p}, nil
}

func (s *Store) AssetRemoteURL() string { return URL(s.Path) }

// All paths below are scoped through os.Root, including symlink resolution.
func read(r *os.Root, name string, limit int64) ([]byte, error) {
	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid or oversized local repository file")
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}

func current(r *os.Root) (string, error) {
	data, err := read(r, directory+"/FORMAT", 128)
	if errors.Is(err, os.ErrNotExist) {
		if _, e := r.Lstat(directory); errors.Is(e, os.ErrNotExist) {
			return "", nil
		}
		return "", errors.New("storage directory has no local FORMAT marker; incomplete copy or incompatible Drive API repository")
	}
	if err != nil {
		return "", err
	}
	if string(data) != format {
		return "", errors.New("unsupported local repository format")
	}
	data, err = read(r, directory+"/CURRENT", 100)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("missing CURRENT pointer; finish copying/syncing the entire folder")
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "unborn" {
		return "", nil
	}
	if !objectID(id) {
		return "", errors.New("invalid local CURRENT pointer")
	}
	return id, nil
}

func objectID(id string) bool {
	if len(id) != 68 || !strings.HasPrefix(id, "obj-") {
		return false
	}
	_, err := hex.DecodeString(id[4:])
	return err == nil && id == strings.ToLower(id)
}

func (s *Store) Load(ctx context.Context) (*repository.Manifest, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	r, err := os.OpenRoot(s.Path)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	id, err := current(r)
	if err != nil {
		return nil, "", err
	}
	if id == "" {
		s.version, s.archives = "", nil
		return repository.Empty(), "", nil
	}
	data, err := read(r, directory+"/"+id, repository.MaxManifestSize)
	if err != nil {
		return nil, "", fmt.Errorf("read current manifest; finish copying/syncing the entire folder: %w", err)
	}
	if digestID(data) != id {
		return nil, "", errors.New("local manifest checksum mismatch")
	}
	var m repository.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, "", err
	}
	if m.Root != "local" || m.Directory != "storage" {
		return nil, "", errors.New("manifest is not a portable local repository")
	}
	if err := m.Validate(); err != nil {
		return nil, "", err
	}
	for _, p := range m.Packs {
		if err := checkObject(r, p.ID, p.Size); err != nil {
			return nil, "", err
		}
	}
	for _, a := range m.Assets {
		if err := checkObject(r, a.ID, a.Size); err != nil {
			return nil, "", err
		}
	}
	for kind, id := range m.ArchiveDirectories {
		if id != archiveDir(kind) {
			return nil, "", errors.New("invalid local archive directory")
		}
	}
	s.version, s.archives = id, maps.Clone(m.ArchiveDirectories)
	return &m, id, nil
}

func checkObject(r *os.Root, id string, size int64) error {
	if !objectID(id) {
		return errors.New("invalid local object ID")
	}
	info, err := r.Stat(directory + "/" + id)
	if err != nil {
		return fmt.Errorf("missing local object; finish copying/syncing the entire folder: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("local object size mismatch; incomplete or corrupt copy")
	}
	return nil
}

func digestID(data []byte) string { return fmt.Sprintf("obj-%x", sha256.Sum256(data)) }

func lock(r *os.Root) (func(), error) {
	if err := r.Mkdir(lockName, 0700); err != nil {
		return nil, fmt.Errorf("local repository locked or unwritable (remove %s only after confirming no writer is running): %w", lockName, err)
	}
	return func() { _ = r.Remove(lockName) }, nil
}

func initialize(r *os.Root) error {
	if _, err := r.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		if err := r.Mkdir(directory, 0700); err != nil {
			return err
		}
		if err := replace(context.Background(), r, directory+"/CURRENT", strings.NewReader("unborn\n"), 7); err != nil {
			return err
		}
		return replace(context.Background(), r, directory+"/FORMAT", strings.NewReader(format), int64(len(format)))
	}
	_, err := current(r)
	return err
}

// replace stages, flushes, then renames within the destination directory. A
// failed copy leaves the published file intact. Callers serialize mutable writes.
func replace(ctx context.Context, r *os.Root, name string, src io.Reader, size int64) error {
	tmp, err := stage(ctx, r, filepath.Dir(name), src, size)
	if err != nil {
		return err
	}
	defer r.Remove(tmp)
	return r.Rename(tmp, name)
}

func stage(ctx context.Context, r *os.Root, dir string, src io.Reader, size int64) (tmp string, err error) {
	if size < 0 || size == 1<<63-1 {
		return "", errors.New("invalid local file size")
	}
	tmp = filepath.Join(dir, ".tmp-"+rand.Text())
	f, err := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = r.Remove(tmp)
		}
	}()
	n, err := io.Copy(f, io.LimitReader(&contextReader{ctx, src}, size+1))
	if err == nil && n != size {
		err = errors.New("local transfer size mismatch")
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return tmp, err
	}
	if err := ctx.Err(); err != nil {
		return tmp, err
	}
	return tmp, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *Store) Upload(ctx context.Context, name string, reader io.ReadSeeker, size int64) (id string, err error) {
	t := progress.Start(ctx, "Writing "+name, size)
	defer func() { t.Finish(err) }()
	r, err := os.OpenRoot(s.Path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	unlock, err := lock(r)
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := initialize(r); err != nil {
		return "", err
	}
	// Hash the exact staged bytes once, then publish their immutable name.
	h := sha256.New()
	tmp, err := stage(ctx, r, directory, io.TeeReader(t.Reader(reader, 0), h), size)
	if err != nil {
		return "", err
	}
	defer r.Remove(tmp)
	id = "obj-" + hex.EncodeToString(h.Sum(nil))
	if _, err := r.Lstat(directory + "/" + id); err == nil {
		// Reuse immutable content only after verifying the existing bytes.
		if err := s.Download(ctx, repository.Pack{ID: id, Size: size}, io.Discard); err != nil {
			return "", err
		}
		return id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := r.Rename(tmp, directory+"/"+id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) Download(ctx context.Context, p repository.Pack, out io.Writer) error {
	r, err := os.OpenRoot(s.Path)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := checkObject(r, p.ID, p.Size); err != nil {
		return err
	}
	f, err := r.Open(directory + "/" + p.ID)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(&contextReader{ctx, f}, p.Size+1))
	if err != nil {
		return err
	}
	if n != p.Size || "obj-"+hex.EncodeToString(h.Sum(nil)) != p.ID {
		return errors.New("local object checksum mismatch")
	}
	return nil
}

func (s *Store) Publish(ctx context.Context, expected string, m *repository.Manifest) error {
	m.Root, m.Directory = "local", "storage"
	m.ArchiveDirectories = maps.Clone(s.archives)
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
	r, err := os.OpenRoot(s.Path)
	if err != nil {
		return err
	}
	defer r.Close()
	unlock, err := lock(r)
	if err != nil {
		return err
	}
	defer unlock()
	v, err := current(r)
	if err != nil {
		return err
	}
	if v != expected {
		return repository.ErrConflict
	}
	for _, p := range m.Packs {
		if err := checkObject(r, p.ID, p.Size); err != nil {
			return err
		}
	}
	for _, a := range m.Assets {
		if err := checkObject(r, a.ID, a.Size); err != nil {
			return err
		}
	}
	if err := replace(ctx, r, directory+"/CURRENT", strings.NewReader(id+"\n"), int64(len(id)+1)); err != nil {
		return err
	}
	s.version = id
	return nil
}

func archiveDir(kind string) string {
	if kind == "branch" {
		return "branches"
	}
	if kind == "tags" {
		return "tags"
	}
	return ""
}

func (s *Store) UploadArchive(ctx context.Context, kind, name string, reader io.ReadSeeker, size int64) (id string, err error) {
	dir := archiveDir(kind)
	if dir == "" || name == "" || strings.ContainsAny(name, "/\\\x00") || !strings.HasSuffix(name, ".zip") {
		return "", errors.New("invalid local ZIP path")
	}
	t := progress.Start(ctx, "Writing ZIP "+name, size)
	defer func() { t.Finish(err) }()
	r, err := os.OpenRoot(s.Path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	unlock, err := lock(r)
	if err != nil {
		return "", err
	}
	defer unlock()
	v, err := current(r)
	if err != nil {
		return "", err
	}
	if v == "" || v != s.version {
		return "", repository.ErrConflict
	}
	// Adopt only directories marked by this backend, never unrelated user data.
	if _, err := r.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		if err := r.Mkdir(dir, 0700); err != nil {
			return "", err
		}
		if err := replace(ctx, r, dir+"/FORMAT", strings.NewReader(format), int64(len(format))); err != nil {
			return "", err
		}
	}
	marker, err := read(r, dir+"/FORMAT", 128)
	if err != nil || string(marker) != format {
		return "", errors.New("ZIP directory is unmarked or incompatible")
	}
	if err := replace(ctx, r, dir+"/"+name, t.Reader(reader, 0), size); err != nil {
		return "", err
	}
	if s.archives == nil {
		s.archives = map[string]string{}
	}
	s.archives[kind] = dir
	return digestID([]byte(dir + "/" + name)), nil
}
