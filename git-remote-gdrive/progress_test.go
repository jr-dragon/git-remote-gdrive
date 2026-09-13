package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitProgress(t *testing.T) {
	d := newMockDrive(t)
	env := testEnvironment(t, d)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(dir string, args ...string) string { return gitCommand(t, dir, env, true, args...) }
	git(root, "init", "-b", "main", source)
	git(source, "gdrive", "install")
	if err := os.WriteFile(filepath.Join(source, ".gitattributes"), []byte("*.bin filter=gdrive-assets -text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "app.bin"), []byte("asset bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "hello"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", ".")
	git(source, "commit", "-m", "initial")
	git(source, "remote", "add", "origin", "gdrive://root")
	d.mu.Lock()
	d.interruptUpload = true
	d.mu.Unlock()
	output := git(source, "push", "--progress", "origin", "main")
	for _, want := range []string{"gdrive: Push: Preparing", "gdrive: Push: Creating Git pack", "file 1/5, Uploading asset", "file 2/5, Uploading Git pack", "gdrive: Push: Refs published", "file 4/5, Uploading ZIP main.zip", "gdrive: Push: 100% (file 5/5"} {
		if !strings.Contains(output, want) {
			t.Fatalf("push missing %q: %s", want, output)
		}
	}
	if !strings.Contains(output, "retrying in") || strings.Count(output, "Uploading Git pack, current file 100%") != 1 {
		t.Fatal("resumed upload progress was not reported correctly", output)
	}
	if strings.Index(output, "Refs published") > strings.Index(output, "Uploading ZIP") {
		t.Fatal("progress reordered ZIP publication")
	}
	if strings.Count(output, "file 1/5") < 2 || strings.Count(output, "file 2/5") < 2 || strings.Count(output, "file 3/5") < 2 || strings.Count(output, "file 4/5") < 2 || strings.Count(output, "file 5/5") < 2 {
		t.Fatal("push did not retain one task across all files", output)
	}
	clone := filepath.Join(root, "clone")
	output = git(root, "clone", "--progress", "gdrive://root", clone)
	for _, want := range []string{"gdrive: Fetch: 100% (file 1/1, Receiving Git pack", "gdrive: Fetch: Fetch complete"} {
		if !strings.Contains(output, want) {
			t.Fatalf("clone missing %q: %s", want, output)
		}
	}
	git(source, "commit", "--allow-empty", "-m", "second")
	output = git(source, "push", "--quiet", "origin", "main")
	if strings.Contains(output, "gdrive: ") {
		t.Fatalf("quiet push emitted progress: %s", output)
	}
	output = git(clone, "pull", "--progress", "--ff-only", "origin", "main")
	if !strings.Contains(output, "gdrive: Fetch: 100% (file 1/1, Receiving Git pack") || !strings.Contains(output, "gdrive: Fetch: Fetch complete") {
		t.Fatal("pull progress missing", output)
	}
	git(source, "commit", "--allow-empty", "-m", "third")
	output = git(source, "push", "--no-progress", "origin", "main")
	if strings.Contains(output, "gdrive: ") {
		t.Fatalf("--no-progress ignored: %s", output)
	}
	output = git(clone, "pull", "--quiet", "--ff-only", "origin", "main")
	if strings.Contains(output, "gdrive: ") {
		t.Fatalf("quiet pull emitted progress: %s", output)
	}
	output = git(root, "clone", "--quiet", "gdrive://root", filepath.Join(root, "quiet-clone"))
	if strings.Contains(output, "gdrive: ") {
		t.Fatalf("quiet clone emitted progress: %s", output)
	}
	git(source, "commit", "--allow-empty", "-m", "failed push")
	d.mu.Lock()
	d.failUpload = true
	d.mu.Unlock()
	output = gitCommand(t, source, env, false, "push", "--progress", "origin", "main")
	if strings.Contains(output, "Uploading Git pack, current file 100%") || strings.Contains(output, "Refs published") || !strings.Contains(output, ", failed") {
		t.Fatal("failed push reported completion", output)
	}
}
