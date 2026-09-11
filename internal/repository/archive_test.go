package repository

import (
	"strings"
	"testing"
)

func TestArchiveLocation(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, tc := range []struct{ ref, kind, name string }{
		{"refs/heads/main", "branch", "main"},
		{"refs/heads/feature/a", "branch", "feature%2Fa"},
		{"refs/heads/feature%2Fa", "branch", "feature%252Fa"},
		{"refs/tags/release/v1", "tags", "release%2Fv1"},
	} {
		kind, name, err := ArchiveLocation(tc.ref, oid)
		if err != nil || kind != tc.kind || name != tc.name+".zip" {
			t.Fatalf("%s: %q, %q, %v", tc.ref, kind, name, err)
		}
	}
	for _, ref := range []string{"refs/notes/commits", "HEAD", "refs/heads/../other"} {
		if _, _, err := ArchiveLocation(ref, oid); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
}

func TestArchiveManifestValidation(t *testing.T) {
	oid := strings.Repeat("a", 40)
	m := Empty()
	m.Root, m.Directory = "root", "storage"
	m.Refs["refs/heads/main"] = oid
	m.Packs = []Pack{{ID: "pack", Hash: oid, SHA256: strings.Repeat("b", 64), Size: 32}}
	m.ArchiveDirectories = map[string]string{"branch": "branches"}
	m.Archives = map[string]Archive{"refs/heads/main": {ID: "zip", OID: oid, SHA256: strings.Repeat("c", 64), Size: 22}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.Refs["refs/heads/main"] = strings.Repeat("d", 40)
	if m.Validate() == nil {
		t.Fatal("accepted stale ref archive")
	}
	m.Refs["refs/heads/main"] = oid
	m.ArchiveDirectories["branch"] = "root"
	if m.Validate() == nil {
		t.Fatal("accepted root as archive directory")
	}
}
