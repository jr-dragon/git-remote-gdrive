package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jr-dragon/git-remote-gdrive/internal/assets"
)

func writeAssetFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestAssetsGitRoundTrip(t *testing.T) {
	d := newMockDrive(t)
	env := testEnvironment(t, d)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(dir string, args ...string) string { return gitCommand(t, dir, env, true, args...) }
	git(root, "init", "-b", "main", source)
	git(source, "gdrive", "install")
	git(source, "gdrive", "install") // Installation is idempotent and local.
	writeAssetFile(t, source, ".gitattributes", []byte("*.bin filter=gdrive-assets diff=gdrive-assets merge=gdrive-assets -text\n"))
	first := []byte("first binary\x00\xff\n")
	second := []byte("second binary\x00\xfe\n")
	writeAssetFile(t, source, "sub/file space.bin", first)
	writeAssetFile(t, source, "empty.bin", nil)
	writeAssetFile(t, source, "plain.txt", []byte("ordinary file\n"))
	writeAssetFile(t, source, "ordinary/.gitattributes", []byte("*.bin -filter\n"))
	writeAssetFile(t, source, "ordinary/plain.bin", []byte("unfiltered bytes\n"))
	git(source, "add", ".")
	git(source, "commit", "-m", "first assets")
	firstCommit := git(source, "rev-parse", "HEAD")
	firstPointer := git(source, "show", "HEAD:sub/file space.bin") + "\n"
	p, recognized, err := assets.Parse([]byte(firstPointer))
	if err != nil || !recognized {
		t.Fatal("Git contains asset bytes instead of a pointer", err)
	}
	if len(firstPointer) > assets.MaxPointerSize || git(source, "show", "HEAD:plain.txt") != "ordinary file" {
		t.Fatal("wrong filtering")
	}
	if git(source, "show", "HEAD:ordinary/plain.bin") != "unfiltered bytes" {
		t.Fatal("nested attributes were ignored")
	}
	writeAssetFile(t, source, "sub/file space.bin", second)
	writeAssetFile(t, source, "duplicate.bin", second)
	git(source, "add", ".")
	git(source, "commit", "-m", "updated assets")
	git(source, "remote", "add", "origin", "gdrive://root")
	git(source, "push", "origin", "main")
	m := d.manifest(t)
	if m.Version != 2 || len(m.Assets) != 3 {
		t.Fatalf("expected historical asset, current deduplicated asset, and empty asset: %+v", m.Assets)
	}
	if m.Assets[p.OID].Size != int64(len(first)) {
		t.Fatal("historical asset was not uploaded")
	}
	d.mu.Lock()
	published := false
	for _, event := range d.events {
		if event == "publish" {
			published = true
		}
		if event == "asset" && published {
			t.Error("asset uploaded after refs")
		}
	}
	uploadCount := len(d.uploads)
	d.mu.Unlock()
	git(source, "push", "origin", "main")
	d.mu.Lock()
	if len(d.uploads) != uploadCount {
		t.Error("up-to-date push uploaded more content")
	}
	d.mu.Unlock()
	// An actual ref update with unchanged asset bytes must reuse its file IDs.
	git(source, "commit", "--allow-empty", "-m", "same assets")
	git(source, "push", "origin", "main")
	for oid, a := range m.Assets {
		if d.manifest(t).Assets[oid] != a {
			t.Fatal("asset was uploaded twice")
		}
	}
	clone := filepath.Join(root, "clone")
	git(root, "clone", "--no-checkout", "gdrive://root", clone)
	if gitCommand(t, clone, env, false, "config", "--local", "--get", "filter.gdrive-assets.clean") != "" {
		t.Fatal("clone silently installed filters")
	}
	git(clone, "gdrive", "install")
	git(clone, "checkout", "HEAD", "--", ".")
	got, err := os.ReadFile(filepath.Join(clone, "sub/file space.bin"))
	if err != nil || !bytes.Equal(got, second) {
		t.Fatalf("smudge failed: %q %v", got, err)
	}
	if status := git(clone, "status", "--porcelain"); status != "" {
		t.Fatalf("filtered checkout is dirty: %s", status)
	}
	git(clone, "checkout", firstCommit)
	got, err = os.ReadFile(filepath.Join(clone, "sub/file space.bin"))
	if err != nil || !bytes.Equal(got, first) {
		t.Fatal("historical checkout failed", err)
	}
	git(clone, "checkout", "main")
	git(clone, "fsck", "--full")
	// Copy a repository to a new Drive root with an asset absent from this
	// clone's cache. The helper retrieves history from the source remote.
	cache := assets.Cache{Dir: filepath.Join(clone, ".git", "gdrive-assets")}
	if err := os.Remove(cache.Path(p)); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.files["other"] = &mockFile{ID: "other", MIME: "application/vnd.google-apps.folder", ETag: `"0"`}
	d.mu.Unlock()
	git(clone, "push", "gdrive://other", "main")
	f, err := cache.Open(p)
	if err != nil {
		t.Fatal("cross-remote push did not recover history asset", err)
	}
	f.Close()
}

func TestGlobalAssetInstallAndClone(t *testing.T) {
	d := newMockDrive(t)
	env := testEnvironment(t, d)
	root := t.TempDir()
	for i, item := range env {
		if strings.HasPrefix(item, "GIT_CONFIG_GLOBAL=") {
			env[i] = "GIT_CONFIG_GLOBAL=" + filepath.Join(root, "global.config")
		}
	}
	git := func(dir string, args ...string) string { return gitCommand(t, dir, env, true, args...) }
	git(root, "gdrive", "install", "--global")
	source := filepath.Join(root, "source")
	git(root, "init", "-b", "main", source)
	git(source, "gdrive", "install")
	git(source, "gdrive", "install")
	writeAssetFile(t, source, ".gitattributes", []byte("*.bin filter=gdrive-assets -text\n"))
	writeAssetFile(t, source, "app.bin", []byte("global filter asset"))
	git(source, "add", ".")
	git(source, "commit", "-m", "asset")
	git(source, "push", "gdrive://root", "main")
	clone := filepath.Join(root, "clone")
	git(root, "clone", "gdrive://root", clone)
	data, err := os.ReadFile(filepath.Join(clone, "app.bin"))
	if err != nil || string(data) != "global filter asset" {
		t.Fatal("global filter did not restore clone", err)
	}
	git(clone, "config", "filter.gdrive-assets.clean", "custom-clean")
	output := gitCommand(t, clone, env, false, "gdrive", "install")
	if !strings.Contains(output, "already configured differently") || git(clone, "config", "--get", "filter.gdrive-assets.clean") != "custom-clean" {
		t.Fatal("install replaced custom filter")
	}
}

func TestAssetFailureDoesNotPublishRefs(t *testing.T) {
	d := newMockDrive(t)
	env := testEnvironment(t, d)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(dir string, args ...string) string { return gitCommand(t, dir, env, true, args...) }
	git(root, "init", "-b", "main", source)
	git(source, "gdrive", "install")
	writeAssetFile(t, source, ".gitattributes", []byte("*.bin filter=gdrive-assets -text\n"))
	writeAssetFile(t, source, "app.bin", []byte("asset"))
	git(source, "add", ".")
	git(source, "commit", "-m", "asset")
	p, _, err := assets.Parse([]byte(git(source, "show", "HEAD:app.bin") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	cache := assets.Cache{Dir: filepath.Join(source, ".git", "gdrive-assets")}
	if err := os.WriteFile(cache.Path(p), []byte("wrong"), 0600); err != nil {
		t.Fatal(err)
	}
	git(source, "push", "--dry-run", "gdrive://root", "main")
	output := gitCommand(t, source, env, false, "push", "gdrive://root", "main")
	if !strings.Contains(output, "asset") {
		t.Fatal(output)
	}
	d.mu.Lock()
	if d.commits != 0 || len(d.uploads) != 0 {
		t.Error("missing asset changed remote")
	}
	d.failAsset = true
	d.mu.Unlock()
	// Re-add an existing tracked asset to repair the local cache.
	git(source, "add", "--renormalize", "app.bin")
	gitCommand(t, source, env, false, "push", "gdrive://root", "main")
	d.mu.Lock()
	if d.commits != 0 {
		t.Error("asset upload failure published refs")
	}
	for _, event := range d.events {
		if event == "zip" {
			t.Error("failed asset upload still exported ZIP")
		}
	}
	d.failAsset = false
	d.mu.Unlock()
	git(source, "push", "gdrive://root", "main")
	if len(d.manifest(t).Assets) != 1 {
		t.Fatal("retry did not publish asset")
	}
}

func TestAssetDownloadVerification(t *testing.T) {
	d := newMockDrive(t)
	env := testEnvironment(t, d)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(dir string, args ...string) string { return gitCommand(t, dir, env, true, args...) }
	git(root, "init", "-b", "main", source)
	git(source, "gdrive", "install")
	writeAssetFile(t, source, ".gitattributes", []byte("*.bin filter=gdrive-assets -text\n"))
	writeAssetFile(t, source, "app.bin", []byte("asset bytes"))
	git(source, "add", ".")
	git(source, "commit", "-m", "asset")
	git(source, "push", "gdrive://root", "main")
	m := d.manifest(t)
	var p assets.Pointer
	d.mu.Lock()
	for oid, a := range m.Assets {
		p = assets.Pointer{OID: oid, Size: a.Size}
		d.files[a.ID].Data = []byte("wrong bytes")
	}
	d.mu.Unlock()
	clone := filepath.Join(root, "clone")
	git(root, "clone", "--no-checkout", "gdrive://root", clone)
	git(clone, "gdrive", "install")
	output := gitCommand(t, clone, env, false, "checkout", "HEAD", "--", ".")
	if !strings.Contains(output, "SHA-256 verification") {
		t.Fatalf("missing integrity failure: %s", output)
	}
	cache := assets.Cache{Dir: filepath.Join(clone, ".git", "gdrive-assets")}
	if _, err := os.Stat(cache.Path(p)); !os.IsNotExist(err) {
		t.Fatal("corrupt download cached")
	}
	// Explicit skip-smudge permits a pointer-only checkout without network data.
	skipEnv := append(append([]string(nil), env...), "GIT_GDRIVE_SKIP_SMUDGE=1")
	gitCommand(t, clone, skipEnv, true, "checkout", "HEAD", "--", ".")
	data, err := os.ReadFile(filepath.Join(clone, "app.bin"))
	if err != nil || !bytes.Equal(data, p.Bytes()) {
		t.Fatal("skip smudge did not preserve pointer", err)
	}
}
