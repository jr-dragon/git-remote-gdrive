package repository

import "testing"

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

func TestManifestValidation(t *testing.T) {
	if err := Empty().Validate(); err != nil {
		t.Fatal(err)
	}
	m := Empty()
	m.Version = 2
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
