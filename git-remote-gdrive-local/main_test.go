package main

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jr-dragon/git-remote-gdrive/internal/localstore"
)

func TestLocalGitRoundTrip(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git-gdrive", "git-remote-gdrive-local"} {
		suffix := ""
		if runtime.GOOS == "windows" {
			suffix = ".exe"
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(bin, name+suffix), "../"+name)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %s %v", out, err)
		}
	}
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != "HOME" && key != "USERPROFILE" && key != "PATH" && key != "XDG_CONFIG_HOME" && !strings.HasPrefix(key, "GIT_") {
			env = append(env, value)
		}
	}
	env = append(env, "HOME="+root, "USERPROFILE="+root, "XDG_CONFIG_HOME="+root, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com", "GIT_TERMINAL_PROMPT=0")
	runGit := func(dir string, ok bool, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if (err == nil) != ok {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git := func(dir string, args ...string) string { t.Helper(); return runGit(dir, true, args...) }
	write := func(dir, name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote space 百分比%")
	if err := os.Mkdir(remote, 0700); err != nil {
		t.Fatal(err)
	}
	git(root, "init", "-b", "main", source)
	git(source, "gdrive", "install", "--global")
	write(source, ".gitattributes", "*.bin filter=gdrive-assets -text\n")
	write(source, "app.bin", "asset\x00bytes")
	write(source, "hello", "first")
	git(source, "add", ".")
	git(source, "commit", "-m", "first")
	git(source, "tag", "-a", "v1", "-m", "release")
	git(source, "remote", "add", "origin", localstore.URL(remote))
	output := git(source, "push", "--progress", "origin", "main", "--tags")
	if !strings.Contains(output, "file 1/6") || !strings.Contains(output, "Push: 100%") {
		t.Fatal("missing aggregate progress", output)
	}
	for _, p := range []string{"branches/main.zip", "tags/v1.zip"} {
		z, err := zip.OpenReader(filepath.Join(remote, p))
		if err != nil {
			t.Fatal(err)
		}
		z.Close()
	}
	// Move via a complete file copy, then remove the original source of the remote.
	copied := filepath.Join(root, "downloaded")
	if err := os.CopyFS(copied, os.DirFS(remote)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(remote, remote+"-old"); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "clone")
	git(root, "clone", "--progress", localstore.URL(copied), clone)
	data, err := os.ReadFile(filepath.Join(clone, "app.bin"))
	if err != nil || string(data) != "asset\x00bytes" {
		t.Fatal("copied asset checkout", err, string(data))
	}
	git(clone, "fsck", "--full")
	if git(source, "rev-parse", "HEAD") != git(clone, "rev-parse", "HEAD") {
		t.Fatal("HEAD mismatch")
	}
	git(clone, "rev-parse", "v1^{}")
	write(clone, "hello", "second")
	git(clone, "add", ".")
	git(clone, "commit", "-m", "second")
	store, _ := localstore.New(copied)
	_, before, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	git(clone, "push", "--dry-run", "origin", "main")
	_, after, err := store.Load(context.Background())
	if err != nil || before != after {
		t.Fatal("dry run wrote refs", err)
	}
	git(clone, "push", "origin", "main")
	git(source, "remote", "set-url", "origin", localstore.URL(copied))
	git(source, "pull", "--ff-only", "origin", "main")
	if git(source, "rev-parse", "HEAD") != git(clone, "rev-parse", "HEAD") {
		t.Fatal("pull mismatch")
	}
	z, err := zip.OpenReader(filepath.Join(copied, "branches", "main.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name == "hello" {
			r, _ := f.Open()
			data, _ := io.ReadAll(r)
			r.Close()
			if string(data) != "second" {
				t.Fatal("ZIP not replaced")
			}
		}
	}
	git(source, "push", "origin", ":refs/tags/v1")
	if _, err := os.Stat(filepath.Join(copied, "tags", "v1.zip")); err != nil {
		t.Fatal("deleted convenience ZIP", err)
	}
	// A literal-URL fetch remembers the local asset source for later checkout.
	bare := filepath.Join(root, "literal")
	git(root, "init", "-b", "main", bare)
	git(bare, "fetch", localstore.URL(copied), "main")
	git(bare, "checkout", "-B", "main", "FETCH_HEAD")
	data, err = os.ReadFile(filepath.Join(bare, "app.bin"))
	if err != nil || string(data) != "asset\x00bytes" {
		t.Fatal("literal fetch assets", err)
	}
	// An older local branch must not overwrite published refs without force.
	git(clone, "reset", "--hard", "HEAD~1")
	runGit(clone, false, "push", "origin", "main")
	git(clone, "push", "--force", "origin", "main")
	// Recover missing cached assets from a local source when migrating remotes.
	if err := os.RemoveAll(filepath.Join(clone, ".git", "gdrive-assets", "objects")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "migrated")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	git(clone, "remote", "add", "destination", localstore.URL(destination))
	git(clone, "push", "destination", "main")
	migrated := filepath.Join(root, "migrated-checkout")
	git(root, "clone", localstore.URL(destination), migrated)
	data, err = os.ReadFile(filepath.Join(migrated, "app.bin"))
	if err != nil || string(data) != "asset\x00bytes" {
		t.Fatal("migrated assets", err)
	}
}
