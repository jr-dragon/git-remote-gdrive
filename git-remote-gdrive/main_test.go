package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jr-dragon/git-remote-gdrive/internal/drive"
	"github.com/jr-dragon/git-remote-gdrive/internal/remotehelper"
	"github.com/jr-dragon/git-remote-gdrive/internal/repository"
)

// Git invokes this test executable as the remote helper. Only test code accepts
// the mock endpoint; the installed binary cannot redirect credentials via env.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GDRIVE_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	id, err := remotehelper.FolderID(args[len(args)-1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client := drive.NewClient(&http.Client{Timeout: 10 * time.Second})
	client.BaseURL = os.Getenv("GDRIVE_TEST_URL")
	h := remotehelper.Helper{OpenStore: func(context.Context) (repository.Store, error) { return &drive.Store{Client: client, Root: id}, nil }}
	if err := h.Run(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type mockProperty struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Visibility string `json:"visibility"`
}
type mockFile struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	MIME    string `json:"mimeType"`
	ETag    string `json:"etag"`
	Parents []struct {
		ID string `json:"id"`
	} `json:"parents"`
	Properties []mockProperty `json:"properties"`
	Data       []byte         `json:"-"`
}
type mockUpload struct {
	File  *mockFile
	Bytes []byte
}
type mockDrive struct {
	mu              sync.Mutex
	files           map[string]*mockFile
	uploads         map[string]*mockUpload
	seq             int
	commits         int
	conflict        bool
	failUpload      bool
	interruptUpload bool
	lostCommit      bool
	server          *httptest.Server
}

func newMockDrive(t *testing.T) *mockDrive {
	d := &mockDrive{files: map[string]*mockFile{"root": {ID: "root", Title: "repo", MIME: "application/vnd.google-apps.folder", ETag: `"0"`}}, uploads: map[string]*mockUpload{}}
	d.server = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.server.Close)
	return d
}

func (d *mockDrive) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/drive/v2/files/generateIds" {
		d.seq++
		fmt.Fprintf(w, `{"ids":["file%d"]}`, d.seq)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/session/") {
		u := d.uploads[r.URL.Path]
		if u == nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
			if d.files[u.File.ID] != nil {
				json.NewEncoder(w).Encode(u.File)
				return
			}
			if len(u.Bytes) > 0 {
				w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(u.Bytes)-1))
			}
			w.WriteHeader(308)
			return
		}
		if d.failUpload && strings.HasSuffix(u.File.Title, ".pack") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"upload failed"}`)
			return
		}
		var start, end, total int64
		if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != int64(len(u.Bytes)) {
			w.WriteHeader(400)
			return
		}
		data, _ := io.ReadAll(r.Body)
		if d.interruptUpload && strings.HasSuffix(u.File.Title, ".pack") {
			d.interruptUpload = false
			u.Bytes = append(u.Bytes, data[:len(data)/2]...)
			w.WriteHeader(503)
			return
		}
		u.Bytes = append(u.Bytes, data...)
		if int64(len(u.Bytes)) == total {
			u.File.Data = u.Bytes
			u.File.ETag = `"1"`
			d.files[u.File.ID] = u.File
			json.NewEncoder(w).Encode(u.File)
			return
		}
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(u.Bytes)-1))
		w.WriteHeader(308)
		return
	}
	if r.Method == "POST" {
		var f mockFile
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			w.WriteHeader(400)
			return
		}
		if d.files[f.ID] != nil {
			w.WriteHeader(409)
			return
		}
		if r.URL.Path == "/upload/drive/v2/files" {
			path := "/session/" + f.ID
			d.uploads[path] = &mockUpload{File: &f}
			w.Header().Set("Location", d.server.URL+path)
			w.WriteHeader(200)
			return
		}
		f.ETag = `"1"`
		d.files[f.ID] = &f
		json.NewEncoder(w).Encode(f)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/drive/v2/files/")
	f := d.files[id]
	if f == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == "PATCH" {
		if d.conflict || r.Header.Get("If-Match") != f.ETag {
			w.WriteHeader(412)
			return
		}
		var patch mockFile
		json.NewDecoder(r.Body).Decode(&patch)
		f.Properties = patch.Properties
		d.commits++
		f.ETag = strconv.Quote(strconv.Itoa(d.commits))
		if d.lostCommit {
			d.lostCommit = false
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(f)
		return
	}
	if r.URL.Query().Get("alt") == "media" {
		w.Write(f.Data)
		return
	}
	json.NewEncoder(w).Encode(f)
}

func (d *mockDrive) manifest(t *testing.T) repository.Manifest {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.files["root"].Properties {
		if p.Key == "gdrive-manifest" {
			var m repository.Manifest
			if err := json.Unmarshal(d.files[p.Value].Data, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
	t.Fatal("no published manifest")
	return repository.Manifest{}
}

func gitCommand(t *testing.T, dir string, env []string, wantOK bool, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if (err == nil) != wantOK {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func testEnvironment(t *testing.T, d *mockDrive) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Git integration wrapper requires a POSIX shell")
	}
	bin := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	wrapper := "#!/bin/sh\nexec " + quote(executable) + " -test.run=^TestHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git-remote-gdrive"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if key == "PATH" || key == "HOME" || key == "XDG_CONFIG_HOME" || strings.HasPrefix(key, "GIT_") {
			continue
		}
		env = append(env, v)
	}
	return append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com", "GIT_TERMINAL_PROMPT=0", "GDRIVE_TEST_HELPER=1", "GDRIVE_TEST_URL="+d.server.URL)
}

func TestGitRoundTrip(t *testing.T) {
	d := newMockDrive(t)
	env := testEnvironment(t, d)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(dir string, args ...string) string { return gitCommand(t, dir, env, true, args...) }
	git(root, "init", "-b", "trunk", source)
	if err := os.WriteFile(filepath.Join(source, "hello.txt"), []byte("hello Drive\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", ".")
	git(source, "commit", "-m", "initial")
	git(source, "tag", "-a", "v1", "-m", "release")
	git(source, "remote", "add", "origin", "gdrive://root")
	d.mu.Lock()
	d.interruptUpload = true
	d.mu.Unlock()
	git(source, "push", "--atomic", "origin", "trunk", "refs/tags/v1")
	m := d.manifest(t)
	if m.HEAD != "refs/heads/trunk" || len(m.Packs) != 1 || m.Peeled["refs/tags/v1"] == "" {
		t.Fatalf("incorrect manifest: %+v", m)
	}
	listing := git(root, "ls-remote", "--symref", "gdrive://root")
	if !strings.Contains(listing, "ref: refs/heads/trunk\tHEAD") || !strings.Contains(listing, "refs/tags/v1^{}") {
		t.Fatal(listing)
	}
	clone := filepath.Join(root, "clone")
	git(root, "clone", "gdrive://root", clone)
	if got := git(clone, "symbolic-ref", "HEAD"); got != "refs/heads/trunk" {
		t.Fatal(got)
	}
	data, err := os.ReadFile(filepath.Join(clone, "hello.txt"))
	if err != nil || string(data) != "hello Drive\n" {
		t.Fatalf("checkout failed: %s %v", data, err)
	}
	git(clone, "fsck", "--full")
	git(source, "commit", "--allow-empty", "-m", "second")
	second := git(source, "rev-parse", "HEAD")
	git(source, "push", "origin", "trunk")
	if len(d.manifest(t).Packs) != 2 {
		t.Fatal("expected incremental pack")
	}
	git(clone, "fetch", "origin")
	git(clone, "merge", "--ff-only", "origin/trunk")
	if got := git(clone, "rev-parse", "HEAD"); got != second {
		t.Fatal("fetch did not update history")
	}
	git(clone, "commit", "--allow-empty", "-m", "other user")
	git(clone, "push", "origin", "trunk")
	git(source, "commit", "--allow-empty", "-m", "divergent")
	gitCommand(t, source, env, false, "push", "origin", "trunk")
	before := d.manifest(t).Refs["refs/heads/trunk"]
	git(source, "push", "--dry-run", "--force", "origin", "trunk")
	if d.manifest(t).Refs["refs/heads/trunk"] != before {
		t.Fatal("dry-run changed refs")
	}
	git(source, "push", "--force", "origin", "trunk")
	git(source, "branch", "feature")
	git(source, "push", "origin", "feature")
	git(source, "push", "origin", ":feature")
	if _, ok := d.manifest(t).Refs["refs/heads/feature"]; ok {
		t.Fatal("branch deletion failed")
	}
	d.mu.Lock()
	d.conflict = true
	d.mu.Unlock()
	git(source, "commit", "--allow-empty", "-m", "conflict")
	before = d.manifest(t).Refs["refs/heads/trunk"]
	gitCommand(t, source, env, false, "push", "origin", "trunk")
	if d.manifest(t).Refs["refs/heads/trunk"] != before {
		t.Fatal("conflicting push changed refs")
	}
	d.mu.Lock()
	d.conflict = false
	d.failUpload = true
	d.mu.Unlock()
	gitCommand(t, source, env, false, "push", "origin", "trunk")
	if d.manifest(t).Refs["refs/heads/trunk"] != before {
		t.Fatal("failed upload changed refs")
	}
	d.mu.Lock()
	d.failUpload = false
	d.lostCommit = true
	d.mu.Unlock()
	git(source, "push", "origin", "trunk")
	// Bound the active pack chain: crossing MaxPacks produces a full snapshot.
	for len(d.manifest(t).Packs) < repository.MaxPacks {
		git(source, "commit", "--allow-empty", "-m", "increment")
		git(source, "push", "origin", "trunk")
	}
	git(source, "commit", "--allow-empty", "-m", "compact")
	git(source, "push", "origin", "trunk")
	if len(d.manifest(t).Packs) != 1 {
		t.Fatal("pack chain was not compacted")
	}
	fresh := filepath.Join(root, "fresh")
	git(root, "clone", "gdrive://root", fresh)
	git(fresh, "fsck", "--full")
	if git(fresh, "rev-parse", "HEAD") != git(source, "rev-parse", "HEAD") {
		t.Fatal("compacted clone differs")
	}
}

func TestCapabilitiesWithoutCredentials(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"origin", "gdrive://root"}, strings.NewReader("capabilities\n\n"), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "fetch\npush\noption\n\n" {
		t.Fatal(output.String())
	}
}

func TestConcurrentInitialization(t *testing.T) {
	d := newMockDrive(t)
	newStore := func() *drive.Store {
		c := drive.NewClient(d.server.Client())
		c.BaseURL = d.server.URL
		return &drive.Store{Client: c, Root: "root"}
	}
	a, b := newStore(), newStore()
	ctx := context.Background()
	ma, va, err := a.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mb, vb, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- a.Publish(ctx, va, ma) }()
	go func() { <-start; results <- b.Publish(ctx, vb, mb) }()
	close(start)
	x, y := <-results, <-results
	if !((x == nil && errors.Is(y, repository.ErrConflict)) || (y == nil && errors.Is(x, repository.ErrConflict))) {
		t.Fatalf("expected one winner: %v, %v", x, y)
	}
	m, _, err := newStore().Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Directory == "" {
		t.Fatal("no canonical repository directory")
	}
}

func TestLargeResumableUpload(t *testing.T) {
	d := newMockDrive(t)
	d.interruptUpload = true
	c := drive.NewClient(d.server.Client())
	c.BaseURL = d.server.URL
	store := &drive.Store{Client: c, Root: "root"}
	if _, _, err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("packed-object-data"), 600000)
	id, err := store.Upload(context.Background(), "test.pack", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !bytes.Equal(d.files[id].Data, data) {
		t.Fatal("resumed upload corrupted content")
	}
}

func TestEmptyAndCorruptRemote(t *testing.T) {
	d := newMockDrive(t)
	env := testEnvironment(t, d)
	root := t.TempDir()
	git := func(dir string, args ...string) string { return gitCommand(t, dir, env, true, args...) }
	if listing := git(root, "ls-remote", "gdrive://root"); listing != "" {
		t.Fatal(listing)
	}
	empty := filepath.Join(root, "empty")
	git(root, "clone", "gdrive://root", empty)
	d.mu.Lock()
	if len(d.files) != 1 {
		t.Error("read operation initialized remote")
	}
	d.mu.Unlock()
	git(empty, "checkout", "-b", "main")
	git(empty, "commit", "--allow-empty", "-m", "initial")
	git(empty, "push", "origin", "main")
	m := d.manifest(t)
	d.mu.Lock()
	d.files[m.Packs[0].ID].Data[12] ^= 1
	d.mu.Unlock()
	output := gitCommand(t, root, env, false, "clone", "gdrive://root", filepath.Join(root, "corrupt"))
	if !strings.Contains(output, "SHA-256 verification") {
		t.Fatal(output)
	}
}
