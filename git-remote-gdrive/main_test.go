package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	h := remotehelper.Helper{Diagnostics: os.Stderr, OpenStore: func(context.Context) (repository.Store, error) { return &drive.Store{Client: client, Root: id}, nil }}
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
	File     *mockFile
	Bytes    []byte
	Complete bool
}
type mockDrive struct {
	mu                  sync.Mutex
	files               map[string]*mockFile
	uploads             map[string]*mockUpload
	seq                 int
	commits             int
	conflict            bool
	failUpload          bool
	failZIP             bool
	failArchiveMetadata bool
	interruptUpload     bool
	interruptZIP        bool
	lostCommit          bool
	events              []string
	server              *httptest.Server
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
	if r.URL.Path == "/drive/v2/files" && r.Method == "GET" {
		parts := regexp.MustCompile(`^'([^']+)' in parents and title = '([^']+)' and trashed = false$`).FindStringSubmatch(r.URL.Query().Get("q"))
		if len(parts) != 3 {
			w.WriteHeader(400)
			return
		}
		items := []*mockFile{}
		for _, f := range d.files {
			if f.Title == parts[2] && len(f.Parents) == 1 && f.Parents[0].ID == parts[1] {
				items = append(items, f)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/upload/drive/v2/files/") && r.Method == "PUT" {
		id := strings.TrimPrefix(r.URL.Path, "/upload/drive/v2/files/")
		old := d.files[id]
		if old == nil {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-Match") != old.ETag {
			w.WriteHeader(412)
			return
		}
		var metadata mockFile
		if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
			w.WriteHeader(400)
			return
		}
		f := *old
		f.Title, f.MIME = metadata.Title, metadata.MIME
		if f.MIME == "application/zip" {
			d.events = append(d.events, "zip")
		}
		d.seq++
		f.ETag = strconv.Quote(strconv.Itoa(d.seq))
		path := fmt.Sprintf("/session/update-%d", d.seq)
		d.uploads[path] = &mockUpload{File: &f}
		w.Header().Set("Location", d.server.URL+path)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/session/") {
		u := d.uploads[r.URL.Path]
		if u == nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
			if u.Complete {
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
		if d.failZIP && strings.HasSuffix(u.File.Title, ".zip") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"ZIP upload failed"}`)
			return
		}
		var start, end, total int64
		if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != int64(len(u.Bytes)) {
			w.WriteHeader(400)
			return
		}
		data, _ := io.ReadAll(r.Body)
		if (d.interruptUpload && strings.HasSuffix(u.File.Title, ".pack")) || (d.interruptZIP && strings.HasSuffix(u.File.Title, ".zip")) {
			d.interruptUpload = false
			d.interruptZIP = false
			u.Bytes = append(u.Bytes, data[:len(data)/2]...)
			w.WriteHeader(503)
			return
		}
		u.Bytes = append(u.Bytes, data...)
		if int64(len(u.Bytes)) == total {
			u.File.Data = u.Bytes
			if u.File.ETag == "" {
				u.File.ETag = `"1"`
			}
			d.files[u.File.ID] = u.File
			u.Complete = true
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
			if f.MIME == "application/zip" {
				d.events = append(d.events, "zip")
			}
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
		if d.failArchiveMetadata {
			for _, p := range patch.Properties {
				if p.Key == "gdrive-manifest" {
					var candidate repository.Manifest
					if uploaded := d.files[p.Value]; uploaded != nil {
						_ = json.Unmarshal(uploaded.Data, &candidate)
					}
					if len(candidate.Archives) > 0 {
						w.WriteHeader(412)
						return
					}
				}
			}
		}
		f.Properties = patch.Properties
		d.events = append(d.events, "publish")
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

func (d *mockDrive) archiveContents(t *testing.T, m repository.Manifest, ref string) map[string]string {
	t.Helper()
	d.mu.Lock()
	archive, ok := m.Archives[ref]
	if !ok {
		d.mu.Unlock()
		t.Fatalf("no archive for %s", ref)
	}
	f := d.files[archive.ID]
	if f == nil {
		d.mu.Unlock()
		t.Fatal("archive not uploaded")
	}
	kind, name, err := repository.ArchiveLocation(ref, m.Refs[ref])
	if err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	dir := d.files[m.ArchiveDirectories[kind]]
	if dir == nil || dir.Title != kind || len(dir.Parents) != 1 || dir.Parents[0].ID != "root" || len(f.Parents) != 1 || f.Parents[0].ID != dir.ID || f.Title != name || f.MIME != "application/zip" {
		d.mu.Unlock()
		t.Fatalf("wrong archive placement/name/MIME for %s", ref)
	}
	data := append([]byte(nil), f.Data...)
	d.mu.Unlock()
	digest := sha256.Sum256(data)
	if int64(len(data)) != archive.Size || hex.EncodeToString(digest[:]) != archive.SHA256 {
		t.Fatal("ZIP checksum/size mismatch")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	contents := make(map[string]string)
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		contents[entry.Name] = string(body)
	}
	return contents
}

func TestPushArchives(t *testing.T) {
	d := newMockDrive(t)
	env := testEnvironment(t, d)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(dir string, args ...string) string { return gitCommand(t, dir, env, true, args...) }
	git(root, "init", "-b", "main", source)
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("hello.txt", "committed\n")
	if err := os.Mkdir(filepath.Join(source, "subdir"), 0700); err != nil {
		t.Fatal(err)
	}
	write("subdir/nested.txt", "nested\n")
	git(source, "add", ".")
	git(source, "commit", "-m", "initial")
	git(source, "tag", "-a", "v1", "-m", "annotated")
	git(source, "tag", "lightweight")
	git(source, "remote", "add", "origin", "gdrive://root")
	// Archive the pushed ref even when it differs from local HEAD and when the
	// worktree contains modified/untracked files. Run from a subdirectory too.
	write("hello.txt", "second\n")
	git(source, "add", "hello.txt")
	git(source, "commit", "-m", "second")
	write("hello.txt", "uncommitted\n")
	write("untracked.txt", "untracked\n")
	git(filepath.Join(source, "subdir"), "push", "origin", "HEAD:refs/heads/feature/a", "refs/tags/v1", "refs/tags/lightweight")
	m := d.manifest(t)
	branch := d.archiveContents(t, m, "refs/heads/feature/a")
	d.mu.Lock()
	if len(d.events) < 3 || d.events[0] != "publish" || d.events[1] != "zip" || d.events[len(d.events)-1] != "publish" {
		t.Errorf("expected ref publication, ZIP uploads, metadata publication: %v", d.events)
	}
	d.mu.Unlock()
	if len(branch) != 2 || branch["hello.txt"] != "second\n" || branch["subdir/nested.txt"] != "nested\n" {
		t.Fatalf("incorrect branch snapshot: %v", branch)
	}
	for _, ref := range []string{"refs/tags/v1", "refs/tags/lightweight"} {
		contents := d.archiveContents(t, m, ref)
		if len(contents) != 2 || contents["hello.txt"] != "committed\n" {
			t.Fatalf("incorrect tag snapshot: %v", contents)
		}
	}
	oldBranch, oldTag := m.Archives["refs/heads/feature/a"], m.Archives["refs/tags/v1"]
	branchDir, tagDir := m.ArchiveDirectories["branch"], m.ArchiveDirectories["tags"]
	write("hello.txt", "third\n")
	git(source, "add", "hello.txt")
	git(source, "commit", "-m", "third")
	d.mu.Lock()
	filesBefore := len(d.files)
	uploadsBefore := len(d.uploads)
	d.mu.Unlock()
	git(source, "push", "--dry-run", "origin", "HEAD:refs/heads/feature/a")
	d.mu.Lock()
	if len(d.files) != filesBefore || len(d.uploads) != uploadsBefore {
		t.Error("dry-run wrote remote ZIPs")
	}
	d.failZIP = true
	d.mu.Unlock()
	output := git(source, "push", "origin", "HEAD:refs/heads/feature/a")
	if !strings.Contains(output, "warning: refs were updated, but ZIP export is incomplete") {
		t.Fatalf("missing ZIP failure warning: %s", output)
	}
	failed := d.manifest(t)
	if _, exists := failed.Archives["refs/heads/feature/a"]; exists {
		t.Fatal("failed ZIP advertised as current")
	}
	if failed.Refs["refs/heads/feature/a"] != git(source, "rev-parse", "HEAD") {
		t.Fatal("ZIP failure rolled back successful refs")
	}
	if got := d.archiveContents(t, m, "refs/heads/feature/a")["hello.txt"]; got != "second\n" {
		t.Fatal("failed ZIP upload altered old file")
	}
	git(source, "commit", "--allow-empty", "-m", "after ZIP failure")
	d.mu.Lock()
	d.failZIP = false
	d.conflict = true
	eventsBefore := len(d.events)
	d.mu.Unlock()
	gitCommand(t, source, env, false, "push", "origin", "HEAD:refs/heads/feature/a")
	if d.manifest(t).Refs["refs/heads/feature/a"] != failed.Refs["refs/heads/feature/a"] {
		t.Fatal("conflict changed published ref")
	}
	d.mu.Lock()
	if len(d.events) != eventsBefore {
		t.Error("ref conflict still attempted ZIP upload")
	}
	d.conflict = false
	d.mu.Unlock()
	git(source, "push", "origin", "HEAD:refs/heads/feature/a")
	updated := d.manifest(t)
	if updated.ArchiveDirectories["branch"] != branchDir || updated.ArchiveDirectories["tags"] != tagDir || updated.Archives["refs/tags/v1"] != oldTag {
		t.Fatal("existing directories/unchanged ZIP were not reused")
	}
	if updated.Archives["refs/heads/feature/a"].ID != oldBranch.ID {
		t.Fatal("branch ZIP was duplicated instead of overwritten")
	}
	if got := d.archiveContents(t, updated, "refs/heads/feature/a")["hello.txt"]; got != "third\n" {
		t.Fatal(got)
	}
	git(source, "tag", "-f", "-a", "v1", "-m", "replacement")
	git(source, "push", "--force", "origin", "refs/tags/v1")
	if d.manifest(t).Archives["refs/tags/v1"].ID != oldTag.ID {
		t.Fatal("tag ZIP was duplicated instead of overwritten")
	}
	if got := d.archiveContents(t, d.manifest(t), "refs/tags/v1")["hello.txt"]; got != "third\n" {
		t.Fatal("force-updated tag has stale ZIP")
	}
	git(source, "push", "origin", ":refs/heads/feature/a", ":refs/tags/v1")
	deleted := d.manifest(t)
	if _, ok := deleted.Archives["refs/heads/feature/a"]; ok {
		t.Fatal("deleted branch still has current ZIP")
	}
	if _, ok := deleted.Archives["refs/tags/v1"]; ok {
		t.Fatal("deleted tag still has current ZIP")
	}
	d.mu.Lock()
	if d.files[oldBranch.ID] == nil || d.files[oldTag.ID] == nil {
		t.Error("last ZIP was deleted with its ref")
	}
	d.mu.Unlock()
	// Recreating a deleted ref finds its existing ZIP by name, without a current
	// archive entry in the manifest, and overwrites that same Drive file.
	git(source, "push", "origin", "HEAD:refs/heads/feature/a")
	if d.manifest(t).Archives["refs/heads/feature/a"].ID != oldBranch.ID {
		t.Fatal("recreated branch did not reuse same-name ZIP")
	}
	d.mu.Lock()
	zipCount := 0
	for _, f := range d.files {
		if f.MIME == "application/zip" {
			zipCount++
		}
	}
	d.mu.Unlock()
	if zipCount != 3 {
		t.Fatalf("expected one ZIP per name; got %d", zipCount)
	}
	// Tags pointing to blobs remain supported, with their data in one ZIP entry.
	blob := git(source, "rev-parse", "HEAD:hello.txt")
	git(source, "tag", "blob-tag", blob)
	git(source, "push", "origin", "refs/tags/blob-tag")
	if got := d.archiveContents(t, d.manifest(t), "refs/tags/blob-tag")["blob"]; got != "third\n" {
		t.Fatal("blob tag ZIP is incorrect")
	}
}

func TestArchiveOverwriteResumeAndStaleWriter(t *testing.T) {
	d := newMockDrive(t)
	client := drive.NewClient(d.server.Client())
	client.BaseURL = d.server.URL
	s := &drive.Store{Client: client, Root: "root"}
	ctx := context.Background()
	m, version, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte("first ZIP payload")
	id, err := s.UploadArchive(ctx, "branch", "main.zip", bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Publish(ctx, version, m); err != nil {
		t.Fatal(err)
	}
	stale := &drive.Store{Client: client, Root: "root"}
	if _, _, err := stale.Load(ctx); err != nil {
		t.Fatal(err)
	}
	m, version, err = s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated := bytes.Repeat([]byte("updated ZIP data"), 600000)
	d.mu.Lock()
	d.interruptZIP = true
	d.mu.Unlock()
	updatedID, err := s.UploadArchive(ctx, "branch", "main.zip", bytes.NewReader(updated), int64(len(updated)))
	if err != nil {
		t.Fatal(err)
	}
	if updatedID != id {
		t.Fatal("overwrite allocated another file ID")
	}
	if err := s.Publish(ctx, version, m); err != nil {
		t.Fatal(err)
	}
	if _, err := stale.UploadArchive(ctx, "branch", "main.zip", bytes.NewReader(first), int64(len(first))); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("stale writer: %v", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !bytes.Equal(d.files[id].Data, updated) {
		t.Fatal("resume or stale writer corrupted overwritten ZIP")
	}
}

func TestPushSucceedsWhenArchiveMetadataFails(t *testing.T) {
	d := newMockDrive(t)
	d.failArchiveMetadata = true
	env := testEnvironment(t, d)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(dir string, args ...string) string { return gitCommand(t, dir, env, true, args...) }
	git(root, "init", "-b", "main", source)
	git(source, "commit", "--allow-empty", "-m", "initial")
	output := git(source, "push", "gdrive://root", "main")
	if !strings.Contains(output, "warning: refs were updated, but ZIP export is incomplete: save ZIP metadata") {
		t.Fatalf("missing metadata warning: %s", output)
	}
	m := d.manifest(t)
	if m.Refs["refs/heads/main"] != git(source, "rev-parse", "HEAD") || len(m.Archives) != 0 {
		t.Fatal("metadata failure corrupted ref publication")
	}
	d.mu.Lock()
	if len(d.events) != 2 || d.events[0] != "publish" || d.events[1] != "zip" {
		t.Errorf("unexpected publication order: %v", d.events)
	}
	d.mu.Unlock()
	clone := filepath.Join(root, "clone")
	git(root, "clone", "gdrive://root", clone)
	if git(clone, "rev-parse", "HEAD") != m.Refs["refs/heads/main"] {
		t.Fatal("ZIP metadata failure prevented clone")
	}
}
