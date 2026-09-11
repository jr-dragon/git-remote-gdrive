package googleauth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/oauth2"
)

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "credential")
	c := &Credential{ClientID: "client", ClientSecret: "secret", Token: &oauth2.Token{AccessToken: "access", RefreshToken: "refresh"}}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want {
				t.Fatalf("mode %o, want %o", info.Mode().Perm(), want)
			}
		}
	}
	if err := Save(path, &Credential{}); err == nil {
		t.Fatal("saved invalid credential")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Token.RefreshToken != "refresh" || loaded.ClientSecret != "secret" {
		t.Fatal("credential changed after failed save")
	}
	c.Token.AccessToken = "replacement"
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil || loaded.Token.AccessToken != "replacement" {
		t.Fatal("replacement failed")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("temporary credentials left behind")
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	got, err := DefaultPath()
	if err != nil || got != filepath.Join(home, ".config", "git-remote-drive", "credential") {
		t.Fatalf("unexpected path %q: %v", got, err)
	}
}
