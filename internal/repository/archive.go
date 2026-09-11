package repository

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// ArchiveLocation uses the destination ref, not the local branch name. Escaping
// is reversible and keeps feature/a distinct from feature%2Fa and feature-a.
func ArchiveLocation(ref, oid string) (kind, name string, err error) {
	if !ValidRef(ref) || !ValidOID(oid) {
		return "", "", errors.New("invalid archive ref or object ID")
	}
	var short string
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		kind, short = "branch", strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		kind, short = "tags", strings.TrimPrefix(ref, "refs/tags/")
	default:
		return "", "", errors.New("only branches and tags have ZIP archives")
	}
	return kind, url.PathEscape(short) + ".zip", nil
}

// MakeArchive snapshots the exact resolved object, never the working tree.
// The ZIP is streamed to disk and uploaded with the same bounded resumable
// transport as packs. Annotated tags are dereferenced by Git.
func (g Git) MakeArchive(ctx context.Context, store Store, ref, oid string) (Archive, error) {
	kind, name, err := ArchiveLocation(ref, oid)
	if err != nil {
		return Archive{}, err
	}
	f, err := os.CreateTemp("", "gdrive-archive-*.zip")
	if err != nil {
		return Archive{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	peeled := g.Peel(ctx, oid)
	objectType, err := g.Type(ctx, peeled)
	if err != nil {
		return Archive{}, err
	}
	var stderr bytes.Buffer
	if objectType == "blob" {
		// Git permits tags to point directly to blobs, which git archive does
		// not accept as a tree-ish. Keep that workflow usable with a single file.
		archive := zip.NewWriter(f)
		entry, err := archive.Create("blob")
		if err != nil {
			return Archive{}, err
		}
		cmd := g.command(ctx, "cat-file", "blob", peeled)
		cmd.Stdout, cmd.Stderr = entry, &stderr
		if err := cmd.Run(); err != nil {
			_ = archive.Close()
			return Archive{}, fmt.Errorf("archive blob: %s", strings.TrimSpace(stderr.String()))
		}
		if err := archive.Close(); err != nil {
			return Archive{}, err
		}
	} else {
		cmd := g.command(ctx, "archive", "--format=zip", oid)
		cmd.Stdout, cmd.Stderr = f, &stderr
		if err := cmd.Run(); err != nil {
			return Archive{}, fmt.Errorf("archive %s: %s", ref, strings.TrimSpace(stderr.String()))
		}
	}
	info, err := f.Stat()
	if err != nil {
		return Archive{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Archive{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return Archive{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Archive{}, err
	}
	id, err := store.UploadArchive(ctx, kind, name, f, info.Size())
	if err != nil {
		return Archive{}, fmt.Errorf("upload ZIP for %s: %w", ref, err)
	}
	return Archive{ID: id, OID: oid, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: info.Size()}, nil
}
