package repository

import (
	"strings"
	"testing"
)

func TestRefValidation(t *testing.T) {
	for _, ref := range []string{"HEAD", "refs/heads/../bad", "refs/heads/.hidden", "refs/heads/name.lock", "refs/heads/name\nerror", "refs/heads/a b", "refs/heads/a//b", "refs/heads/a@{b", "refs/heads/a~1", "refs/heads/a:", "refs/heads/a."} {
		if ValidRef(ref) {
			t.Errorf("accepted %q", ref)
		}
	}
	for _, ref := range []string{"refs/heads/main", "refs/heads/feature/example", "refs/tags/v1.0", "refs/notes/commits"} {
		if !ValidRef(ref) {
			t.Errorf("rejected %q", ref)
		}
	}
}

func TestAssetManifestValidation(t *testing.T) {
	digest := strings.Repeat("a", 64)
	m := Empty()
	m.Assets = map[string]Asset{digest: {ID: "asset1", Size: 1}}
	if m.Validate() == nil {
		t.Fatal("v1 accepted required assets")
	}
	m.Version = 2
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Asset{{ID: "../bad", Size: 1}, {ID: "asset1", Size: -1}, {ID: "asset1", Size: 1<<63 - 1}} {
		m.Assets[digest] = a
		if m.Validate() == nil {
			t.Fatal("accepted invalid asset", a)
		}
	}
	m.Assets = map[string]Asset{"bad": {ID: "asset1", Size: 1}}
	if m.Validate() == nil {
		t.Fatal("accepted invalid digest")
	}
}

func TestManifestValidation(t *testing.T) {
	if err := Empty().Validate(); err != nil {
		t.Fatal(err)
	}
	m := Empty()
	m.Version = 3
	if m.Validate() == nil {
		t.Fatal("accepted future format")
	}
	m = Empty()
	m.Refs["refs/heads/main"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if m.Validate() == nil {
		t.Fatal("accepted refs without packs")
	}
	m = Empty()
	m.HEAD = "refs/tags/v1"
	if m.Validate() == nil {
		t.Fatal("accepted invalid HEAD")
	}
}
