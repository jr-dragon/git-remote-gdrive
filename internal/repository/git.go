package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Git struct{ Dir string }

func (g Git) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + g.Dir}, args...)...)
	// Avoid inherited worktree/index settings changing plumbing behavior.
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != "GIT_DIR" && key != "GIT_WORK_TREE" && key != "GIT_INDEX_FILE" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	return cmd
}

func (g Git) output(ctx context.Context, input io.Reader, args ...string) (string, error) {
	cmd := g.command(ctx, args...)
	cmd.Stdin = input
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func (g Git) Check(ctx context.Context) error {
	format, err := g.output(ctx, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	if format != "sha1" {
		return errors.New("repository format v1 supports SHA-1 Git repositories only")
	}
	shallow, err := g.output(ctx, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return err
	}
	if shallow == "true" {
		return errors.New("shallow repositories are not supported")
	}
	return nil
}

func (g Git) Resolve(ctx context.Context, ref string) (string, error) {
	oid, err := g.output(ctx, nil, "rev-parse", "--verify", "--end-of-options", ref+"^{object}")
	if err != nil || !ValidOID(oid) {
		return "", errors.New("cannot resolve push source")
	}
	return oid, nil
}

func (g Git) Head(ctx context.Context) string {
	ref, _ := g.output(ctx, nil, "symbolic-ref", "-q", "HEAD")
	return ref
}
func (g Git) Type(ctx context.Context, oid string) (string, error) {
	return g.output(ctx, nil, "cat-file", "-t", oid)
}
func (g Git) Peel(ctx context.Context, oid string) string {
	value, _ := g.output(ctx, nil, "rev-parse", "--verify", oid+"^{}")
	return value
}
func (g Git) Ancestor(ctx context.Context, old, new string) bool {
	return g.command(ctx, "merge-base", "--is-ancestor", old, new).Run() == nil
}

func (g Git) Hydrate(ctx context.Context, store Store, m *Manifest) error {
	if err := g.Check(ctx); err != nil {
		return err
	}
	for _, pack := range m.Packs {
		idx, err := g.output(ctx, nil, "rev-parse", "--git-path", "objects/pack/pack-"+pack.Hash+".idx")
		if err != nil {
			return err
		}
		if _, err := os.Stat(idx); err == nil {
			if _, err := os.Stat(strings.TrimSuffix(idx, ".idx") + ".pack"); err == nil {
				continue
			}
		}
		if err := g.importPack(ctx, store, pack); err != nil {
			return err
		}
	}
	return g.Connected(ctx, m.Refs)
}

func (g Git) Connected(ctx context.Context, refs map[string]string) error {
	if len(refs) == 0 {
		return nil
	}
	var input strings.Builder
	for _, oid := range refs {
		fmt.Fprintln(&input, oid)
	}
	cmd := g.command(ctx, "rev-list", "--objects", "--stdin", "--missing=error")
	cmd.Stdin = strings.NewReader(input.String())
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("repository object connectivity check failed: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (g Git) importPack(ctx context.Context, store Store, pack Pack) error {
	f, err := os.CreateTemp("", "gdrive-fetch-*.pack")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	if err := store.Download(ctx, pack, io.MultiWriter(f, h)); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != pack.Size || hex.EncodeToString(h.Sum(nil)) != pack.SHA256 {
		return errors.New("downloaded pack failed size/SHA-256 verification")
	}
	trailer := make([]byte, 20)
	if _, err := f.ReadAt(trailer, info.Size()-20); err != nil {
		return err
	}
	if hex.EncodeToString(trailer) != pack.Hash {
		return errors.New("downloaded pack has unexpected Git hash")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = g.output(ctx, f, "index-pack", "--stdin", "--strict")
	return err
}

// MakePack writes either a complete snapshot or only objects not reachable from
// the previous refs. Packs are non-thin; earlier packs supply history, not deltas.
func (g Git) MakePack(ctx context.Context, store Store, previous, next *Manifest, full bool) (*Pack, error) {
	f, err := os.CreateTemp("", "gdrive-push-*.pack")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	var revisions strings.Builder
	for _, ref := range SortedRefs(next.Refs) {
		fmt.Fprintln(&revisions, next.Refs[ref])
	}
	if !full {
		for _, ref := range SortedRefs(previous.Refs) {
			fmt.Fprintln(&revisions, "^"+previous.Refs[ref])
		}
	}
	cmd := g.command(ctx, "pack-objects", "--stdout", "--revs")
	cmd.Stdin = strings.NewReader(revisions.String())
	cmd.Stdout = f
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("create pack: %s", strings.TrimSpace(stderr.String()))
	}
	header := make([]byte, 12)
	if _, err := f.ReadAt(header, 0); err != nil {
		return nil, err
	}
	if string(header[:4]) != "PACK" {
		return nil, errors.New("git produced an invalid pack")
	}
	if binary.BigEndian.Uint32(header[8:]) == 0 {
		return nil, nil
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	trailer := make([]byte, 20)
	if _, err := f.ReadAt(trailer, info.Size()-20); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	pack := &Pack{Hash: hex.EncodeToString(trailer), SHA256: hex.EncodeToString(h.Sum(nil)), Size: info.Size()}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	pack.ID, err = store.Upload(ctx, "pack-"+pack.Hash+".pack", f, pack.Size)
	if err != nil {
		return nil, err
	}
	// Cache the exact uploaded pack in Git's own object store for subsequent runs.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := g.output(ctx, f, "index-pack", "--stdin", "--strict"); err != nil {
		return nil, err
	}
	return pack, nil
}

func SortedRefs(refs map[string]string) []string {
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func DiscoverGitDir(ctx context.Context) (string, error) {
	if dir := os.Getenv("GIT_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return "", errors.New("fetch/push requires a local Git repository")
	}
	return strings.TrimSpace(string(out)), nil
}
