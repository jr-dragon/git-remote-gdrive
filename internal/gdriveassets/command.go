// Package gdriveassets connects optional Git filters to the shared Drive store.
package gdriveassets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jr-dragon/git-remote-gdrive/internal/assets"
	"github.com/jr-dragon/git-remote-gdrive/internal/drive"
	"github.com/jr-dragon/git-remote-gdrive/internal/googleauth"
	"github.com/jr-dragon/git-remote-gdrive/internal/localstore"
	"github.com/jr-dragon/git-remote-gdrive/internal/progress"
	"github.com/jr-dragon/git-remote-gdrive/internal/repository"
)

type OpenStore func(context.Context, string) (repository.Store, error)

func authenticatedStore(ctx context.Context, root string) (repository.Store, error) {
	if strings.HasPrefix(root, "gdrive-local://") {
		path, err := localstore.Path(root)
		if err != nil {
			return nil, err
		}
		return localstore.New(path)
	}
	path, err := googleauth.DefaultPath()
	if err != nil {
		return nil, err
	}
	client, err := googleauth.Client(ctx, path)
	if err != nil {
		return nil, err
	}
	return &drive.Store{Client: drive.NewClient(client), Root: root}, nil
}

func Run(ctx context.Context, args []string, input io.Reader, output io.Writer, open OpenStore) error {
	if len(args) == 0 {
		return errors.New("expected install, asset-clean, or asset-smudge")
	}
	if args[0] == "install" {
		return install(ctx, args[1:], output)
	}
	if len(args) != 1 || (args[0] != "asset-clean" && args[0] != "asset-smudge") {
		return errors.New("invalid asset filter command")
	}
	dir, err := repository.DiscoverGitDir(ctx)
	if err != nil {
		return err
	}
	g := repository.Git{Dir: dir}
	cache, err := g.AssetCache(ctx)
	if err != nil {
		return err
	}
	if args[0] == "asset-clean" {
		return cache.Clean(input, output)
	}
	prefix, err := io.ReadAll(io.LimitReader(input, assets.MaxPointerSize+1))
	if err != nil {
		return err
	}
	p, recognized, err := assets.Parse(prefix)
	if err != nil {
		return err
	}
	if !recognized {
		_, err = io.Copy(output, io.MultiReader(bytes.NewReader(prefix), input))
		return err
	}
	if os.Getenv("GIT_GDRIVE_SKIP_SMUDGE") == "1" {
		_, err = output.Write(prefix)
		return err
	}
	r := Resolver{Git: g, Open: open}
	if err := r.Ensure(ctx, p); err != nil {
		return err
	}
	f, err := cache.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(output, f)
	return err
}

func install(ctx context.Context, args []string, output io.Writer) error {
	scope := "--local"
	if len(args) == 1 && args[0] == "--global" {
		scope = "--global"
	} else if len(args) != 0 {
		return errors.New("usage: git gdrive install [--global]")
	}
	if scope == "--local" {
		if _, err := repository.DiscoverGitDir(ctx); err != nil {
			return errors.New("run git gdrive install inside a repository, or use --global before cloning")
		}
	}
	settings := [][2]string{
		{"filter.gdrive-assets.clean", "git-gdrive asset-clean"},
		{"filter.gdrive-assets.smudge", "git-gdrive asset-smudge"},
		{"filter.gdrive-assets.required", "true"},
		{"diff.gdrive-assets.binary", "true"},
		{"merge.gdrive-assets.name", "gdrive asset binary merge"},
		{"merge.gdrive-assets.driver", "false"},
	}
	// Do not replace a separately configured process driver or custom commands.
	for _, setting := range append(settings, [2]string{"filter.gdrive-assets.process", ""}) {
		cmd := exec.CommandContext(ctx, "git", "config", "--get-all", setting[0])
		value, err := cmd.Output()
		var exit *exec.ExitError
		if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
			return fmt.Errorf("read filter configuration: %w", err)
		}
		for _, existing := range strings.Split(strings.TrimSuffix(string(value), "\n"), "\n") {
			if existing != "" && existing != setting[1] {
				return fmt.Errorf("%s is already configured differently; review that Git setting before installing", setting[0])
			}
		}
	}
	for _, setting := range settings {
		cmd := exec.CommandContext(ctx, "git", "config", scope, "--replace-all", setting[0], setting[1])
		if data, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("install asset filter: %s: %w", strings.TrimSpace(string(data)), err)
		}
	}
	fmt.Fprintln(output, "gdrive-assets filters installed. Track files with filter=gdrive-assets diff=gdrive-assets merge=gdrive-assets -text in .gitattributes.")
	return nil
}

type remote struct {
	store    repository.Store
	manifest *repository.Manifest
	err      error
}

// Resolver caches remote indexes for a push batch. Checkout filters only access
// Drive when a verified object is absent locally; clean is always offline.
type Resolver struct {
	Git     repository.Git
	Open    OpenStore
	remotes map[string]remote
}

func (r *Resolver) Ensure(ctx context.Context, p assets.Pointer) error {
	cache, err := r.Git.AssetCache(ctx)
	if err != nil {
		return err
	}
	if f, err := cache.Open(p); err == nil {
		f.Close()
		return nil
	}
	roots, err := r.roots(ctx, cache)
	if err != nil {
		return err
	}
	if r.Open == nil {
		r.Open = authenticatedStore
	}
	if r.remotes == nil {
		r.remotes = map[string]remote{}
	}
	var failures []error
	for _, root := range roots {
		entry, ok := r.remotes[root]
		if !ok {
			entry.store, entry.err = r.Open(ctx, root)
			if entry.err == nil {
				entry.manifest, _, entry.err = entry.store.Load(ctx)
			}
			r.remotes[root] = entry
		}
		if entry.err != nil {
			failures = append(failures, entry.err)
			continue
		}
		a, ok := entry.manifest.Assets[p.OID]
		if !ok {
			continue
		}
		if a.Size != p.Size {
			failures = append(failures, errors.New("asset size differs from remote index"))
			continue
		}
		if err := receive(ctx, cache, p, entry.store, a); err != nil {
			failures = append(failures, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("asset %s unavailable; fetch from its gdrive remote or restore the original file and git add it again: %w", p.OID, errors.Join(append([]error{errors.New("no verified asset content found")}, failures...)...))
}

func receive(ctx context.Context, cache assets.Cache, p assets.Pointer, store repository.Store, a repository.Asset) error {
	transfer := progress.Start(ctx, "Receiving asset "+p.OID[:12], p.Size)
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := store.Download(ctx, repository.Pack{ID: a.ID, Size: a.Size}, transfer.Writer(writer))
		writer.CloseWithError(err)
		done <- err
	}()
	_, err := cache.Put(reader, &p)
	reader.CloseWithError(err)
	err = errors.Join(err, <-done)
	transfer.Finish(err)
	return err
}

func (r *Resolver) roots(ctx context.Context, cache assets.Cache) ([]string, error) {
	urls, err := r.Git.AssetRemoteURLs(ctx)
	if err != nil {
		return nil, err
	}
	var roots []string
	seen := map[string]bool{}
	add := func(root string) {
		if repository.ValidID(root) && !seen[root] {
			roots = append(roots, root)
			seen[root] = true
		}
	}
	for _, raw := range urls {
		if path, err := localstore.Path(raw); err == nil {
			canonical := localstore.URL(path)
			if !seen[canonical] {
				roots = append(roots, canonical)
				seen[canonical] = true
			}
			continue
		}
		u, err := url.Parse(raw)
		if err == nil && u.Scheme == "gdrive" && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" {
			add(u.Host)
		}
	}
	entries, err := os.ReadDir(filepath.Join(cache.Dir, "remotes"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			add(entry.Name())
		}
	}
	entries, err = os.ReadDir(filepath.Join(cache.Dir, "local-remotes"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cache.Dir, "local-remotes", entry.Name()))
		if err != nil {
			return nil, err
		}
		if path, err := localstore.Path(string(data)); err == nil {
			canonical := localstore.URL(path)
			if !seen[canonical] {
				roots = append(roots, canonical)
				seen[canonical] = true
			}
		}
	}
	return roots, nil
}
