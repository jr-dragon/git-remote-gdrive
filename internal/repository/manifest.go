// Package repository defines the versioned on-Drive format and Git pack operations.
package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/jr-dragon/git-remote-gdrive/internal/assets"
)

const MaxPacks = 16
const MaxManifestSize = 8 << 20
const DirectoryName = "@.git-remote-gdrive"

var ErrConflict = errors.New("remote changed during push; fetch and retry")
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var oidPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Pack struct {
	ID     string `json:"id"`
	Hash   string `json:"git_hash"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Archive struct {
	ID     string `json:"id"`
	OID    string `json:"oid"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Asset struct {
	ID   string `json:"id"`
	Size int64  `json:"size"`
}

type Manifest struct {
	Format             string             `json:"format"`
	Version            int                `json:"version"`
	ObjectFormat       string             `json:"object_format"`
	Root               string             `json:"root"`
	Directory          string             `json:"directory"`
	HEAD               string             `json:"head"`
	Refs               map[string]string  `json:"refs"`
	Peeled             map[string]string  `json:"peeled,omitempty"`
	Packs              []Pack             `json:"packs"`
	ArchiveDirectories map[string]string  `json:"archive_directories,omitempty"`
	Archives           map[string]Archive `json:"archives,omitempty"`
	Assets             map[string]Asset   `json:"assets,omitempty"`
}

func Empty() *Manifest {
	return &Manifest{Format: "git-remote-gdrive", Version: 1, ObjectFormat: "sha1", HEAD: "refs/heads/main", Refs: map[string]string{}, Peeled: map[string]string{}}
}

func ValidID(id string) bool   { return idPattern.MatchString(id) && len(id) <= 100 }
func ValidOID(oid string) bool { return oidPattern.MatchString(oid) && oid != strings.Repeat("0", 40) }

func ValidRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || strings.ContainsAny(ref, " ~^:?*[\\\x7f") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, r := range ref {
		if r < 32 {
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (m *Manifest) Validate() error {
	if m.Format != "git-remote-gdrive" || (m.Version != 1 && m.Version != 2) || m.ObjectFormat != "sha1" {
		return errors.New("unsupported repository format")
	}
	if !ValidRef(m.HEAD) || !strings.HasPrefix(m.HEAD, "refs/heads/") || m.Refs == nil || len(m.Packs) > MaxPacks {
		return errors.New("invalid repository manifest")
	}
	for ref, oid := range m.Refs {
		if !ValidRef(ref) || !ValidOID(oid) {
			return fmt.Errorf("invalid ref in repository manifest")
		}
		parts := strings.Split(ref, "/")
		for i := 2; i < len(parts); i++ {
			if _, exists := m.Refs[strings.Join(parts[:i], "/")]; exists {
				return errors.New("conflicting ref names")
			}
		}
	}
	for ref, oid := range m.Peeled {
		if !strings.HasPrefix(ref, "refs/tags/") || m.Refs[ref] == "" || !ValidOID(oid) {
			return errors.New("invalid peeled tag")
		}
	}
	seen := map[string]bool{}
	if len(m.Assets) > 0 && m.Version != 2 {
		return errors.New("assets require repository format v2")
	}
	for oid, asset := range m.Assets {
		if !(assets.Pointer{OID: oid, Size: asset.Size}).Valid() || !ValidID(asset.ID) || seen[asset.ID] || asset.ID == m.Root || asset.ID == m.Directory {
			return errors.New("invalid asset descriptor")
		}
		seen[asset.ID] = true
	}
	for _, p := range m.Packs {
		if !ValidID(p.ID) || !oidPattern.MatchString(p.Hash) || !digestPattern.MatchString(p.SHA256) || p.Size < 32 || seen[p.ID] {
			return errors.New("invalid pack descriptor")
		}
		seen[p.ID] = true
	}
	if len(m.Refs) > 0 && len(m.Packs) == 0 {
		return errors.New("repository refs have no packs")
	}
	for kind, id := range m.ArchiveDirectories {
		if (kind != "branch" && kind != "tags") || !ValidID(id) || id == m.Root || id == m.Directory {
			return errors.New("invalid archive directory")
		}
	}
	if id := m.ArchiveDirectories["branch"]; id != "" && id == m.ArchiveDirectories["tags"] {
		return errors.New("branch and tag archives must use different directories")
	}
	for ref, archive := range m.Archives {
		kind, _, err := ArchiveLocation(ref, archive.OID)
		if err != nil || archive.OID != m.Refs[ref] || !ValidID(archive.ID) || !digestPattern.MatchString(archive.SHA256) || archive.Size < 22 || m.ArchiveDirectories[kind] == "" {
			return errors.New("invalid ref archive")
		}
	}
	return nil
}

// Store publishes an immutable manifest with compare-and-swap semantics. Load's
// version must be passed unchanged to Publish; uploads alone never publish refs.
type Store interface {
	Load(context.Context) (*Manifest, string, error)
	Upload(context.Context, string, io.ReadSeeker, int64) (string, error)
	UploadArchive(context.Context, string, string, io.ReadSeeker, int64) (string, error)
	Download(context.Context, Pack, io.Writer) error
	Publish(context.Context, string, *Manifest) error
}
