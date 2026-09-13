package localstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jr-dragon/git-remote-gdrive/internal/repository"
)

func TestURL(t *testing.T) {
	p := filepath.Join(t.TempDir(), "space #百分比%")
	got, err := Path(URL(p))
	if err != nil || got != p {
		t.Fatalf("round trip: %q %v", got, err)
	}
	for _, raw := range []string{"gdrive://root", "gdrive-local://relative/path", "gdrive-local://", "gdrive-local:relative", "gdrive-local:///tmp?x", "gdrive-local:///tmp#x", "gdrive-local:///tmp%00", "gdrive-local:///tmp%ZZ", "gdrive-local:////server/share"} {
		if _, err := Path(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestStorePublicationAndCorruption(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, _ := New(root)
	if _, v, err := s.Load(ctx); err != nil || v != "" {
		t.Fatal(v, err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("reading initialized storage")
	}
	data := []byte("asset content")
	id, err := s.Upload(ctx, "asset", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	m := repository.Empty()
	m.Version = 2
	m.Assets = map[string]repository.Asset{strings.TrimPrefix(digestID(data), "obj-"): {ID: id, Size: int64(len(data))}}
	if err := s.Publish(ctx, "", m); err != nil {
		t.Fatal(err)
	}
	_, version, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Publish(ctx, "", m); !errors.Is(err, repository.ErrConflict) {
		t.Fatal("stale publish:", err)
	}
	// A cancelled transfer must never change CURRENT.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Publish(cancelled, version, m); err == nil {
		t.Fatal("cancelled publication succeeded")
	}
	_, after, err := s.Load(ctx)
	if err != nil || after != version {
		t.Fatal("CURRENT changed", err)
	}
	if err := os.WriteFile(filepath.Join(root, directory, id), []byte("broken object"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.Download(ctx, repository.Pack{ID: id, Size: int64(len(data))}, &out); err == nil {
		t.Fatal("corruption accepted")
	}
	if err := os.Remove(filepath.Join(root, directory, id)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load(ctx); err == nil {
		t.Fatal("incomplete copy accepted")
	}
}

func TestLockAndContainment(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, _ := New(root)
	if err := os.Mkdir(filepath.Join(root, lockName), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upload(ctx, "asset", strings.NewReader("x"), 1); err == nil {
		t.Fatal("ignored lock")
	}
	if err := os.Remove(filepath.Join(root, lockName)); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, directory)); err != nil {
		t.Skip(err)
	}
	if _, err := s.Upload(ctx, "asset", strings.NewReader("x"), 1); err == nil {
		t.Fatal("followed storage symlink outside root")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("modified outside folder")
	}
}

func TestMissingCurrentIsNotEmpty(t *testing.T) {
	s, _ := New(t.TempDir())
	ctx := context.Background()
	if err := s.Publish(ctx, "", repository.Empty()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.Path, directory, "CURRENT")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load(ctx); err == nil {
		t.Fatal("missing CURRENT treated as empty")
	}
	if err := s.Publish(ctx, "", repository.Empty()); err == nil {
		t.Fatal("overwrote incomplete repository")
	}
}

func TestConcurrentPublish(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seed, _ := New(root)
	if err := seed.Publish(ctx, "", repository.Empty()); err != nil {
		t.Fatal(err)
	}
	_, version, err := seed.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, branch := range []string{"left", "right"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := New(root)
			if err != nil {
				results <- err
				return
			}
			m := repository.Empty()
			m.HEAD = "refs/heads/" + branch
			<-start
			results <- s.Publish(ctx, version, m)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected one winner, got %d", succeeded)
	}
	if _, _, err := seed.Load(ctx); err != nil {
		t.Fatal("invalid winning snapshot", err)
	}
}

func TestZIPReplacementAndStaleWrite(t *testing.T) {
	ctx := context.Background()
	s, _ := New(t.TempDir())
	m := repository.Empty()
	if err := s.Publish(ctx, "", m); err != nil {
		t.Fatal(err)
	}
	_, v, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UploadArchive(ctx, "branch", "main.zip", strings.NewReader("first"), 5)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.UploadArchive(ctx, "branch", "main.zip", strings.NewReader("second"), 6)
	if err != nil || id2 != id {
		t.Fatal(id2, err)
	}
	if _, err := s.UploadArchive(ctx, "branch", "main.zip", strings.NewReader("short"), 100); err == nil {
		t.Fatal("short copy accepted")
	}
	data, _ := os.ReadFile(filepath.Join(s.Path, "branches", "main.zip"))
	if string(data) != "second" {
		t.Fatal("failed overwrite destroyed ZIP")
	}
	other, _ := New(s.Path)
	if _, _, err := other.Load(ctx); err != nil {
		t.Fatal(err)
	}
	m.HEAD = "refs/heads/other"
	if err := other.Publish(ctx, v, m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadArchive(ctx, "branch", "main.zip", strings.NewReader("stale"), 5); !errors.Is(err, repository.ErrConflict) {
		t.Fatal("stale ZIP", err)
	}
}
