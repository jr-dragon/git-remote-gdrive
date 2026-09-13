package repository

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jr-dragon/git-remote-gdrive/internal/assets"
	"github.com/jr-dragon/git-remote-gdrive/internal/progress"
)

func (g Git) AssetCache(ctx context.Context) (assets.Cache, error) {
	dir, err := g.output(ctx, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return assets.Cache{Dir: filepath.Join(dir, "gdrive-assets")}, err
}

// AssetPointers scans all reachable history, including blobs behind direct tags.
// Pointer recognition is independent of today's attributes: older commits must
// remain usable after a path is renamed, deleted, or stops using the filter.
// cat-file skips large ordinary blobs without reading their contents into memory.
func (g Git) AssetPointers(ctx context.Context, refs map[string]string) (map[string]assets.Pointer, error) {
	result := map[string]assets.Pointer{}
	if len(refs) == 0 {
		return result, nil
	}
	var revisions strings.Builder
	for _, ref := range SortedRefs(refs) {
		fmt.Fprintln(&revisions, refs[ref])
	}
	objects := g.command(ctx, "rev-list", "--objects", "--no-object-names", "--stdin", "--missing=error")
	objects.Stdin = strings.NewReader(revisions.String())
	list, err := objects.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	objects.Stderr = &stderr
	if err := objects.Start(); err != nil {
		return nil, err
	}
	defer list.Close()
	defer func() {
		if objects.ProcessState == nil {
			_ = objects.Process.Kill()
			_ = objects.Wait()
		}
	}()
	batch := g.command(ctx, "cat-file", "--batch-command")
	in, err := batch.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := batch.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	if err := batch.Start(); err != nil {
		in.Close()
		return nil, err
	}
	defer func() {
		in.Close()
		out.Close()
		if batch.ProcessState == nil {
			_ = batch.Process.Kill()
			_ = batch.Wait()
		}
	}()
	reader := bufio.NewReader(out)
	scanner := bufio.NewScanner(list)
	for scanner.Scan() {
		oid := scanner.Text()
		if !ValidOID(oid) {
			return nil, errors.New("invalid object during asset scan")
		}
		if _, err := fmt.Fprintf(in, "info %s\n", oid); err != nil {
			return nil, err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != oid {
			return nil, errors.New("cannot inspect asset object")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return nil, errors.New("invalid object size")
		}
		if fields[1] != "blob" || size > assets.MaxPointerSize {
			continue
		}
		if _, err := fmt.Fprintf(in, "contents %s\n", oid); err != nil {
			return nil, err
		}
		header, err := reader.ReadString('\n')
		if err != nil || header != line {
			return nil, errors.New("invalid asset object response")
		}
		data := make([]byte, size+1)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, err
		}
		if data[size] != '\n' {
			return nil, errors.New("invalid asset object boundary")
		}
		p, recognized, err := assets.Parse(data[:size])
		if err != nil {
			return nil, err
		}
		if recognized {
			if old, ok := result[p.OID]; ok && old.Size != p.Size {
				return nil, errors.New("conflicting asset pointer sizes")
			}
			result[p.OID] = p
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := objects.Wait(); err != nil {
		return nil, fmt.Errorf("scan asset history: %s", stderr.String())
	}
	if err := in.Close(); err != nil {
		return nil, err
	}
	if err := batch.Wait(); err != nil {
		return nil, err
	}
	return result, nil
}

type AssetPlan struct {
	Pointers map[string]assets.Pointer
	Missing  []string
	Recover  map[string]bool
}

func (p AssetPlan) TransferCount() int {
	total := len(p.Missing)
	for _, oid := range p.Missing {
		if p.Recover[oid] {
			total++
		}
	}
	return total
}

// PlanAssets validates all reachable pointers and determines the exact number of
// source downloads and destination uploads before task progress starts.
func (g Git) PlanAssets(ctx context.Context, next *Manifest) (AssetPlan, error) {
	progress.Step(ctx, "Checking asset history...")
	pointers, err := g.AssetPointers(ctx, next.Refs)
	plan := AssetPlan{Pointers: pointers, Recover: map[string]bool{}}
	if err != nil || len(pointers) == 0 {
		return plan, err
	}
	cache, err := g.AssetCache(ctx)
	if err != nil {
		return plan, err
	}
	for _, oid := range slices.Sorted(maps.Keys(pointers)) {
		p := pointers[oid]
		if existing, ok := next.Assets[oid]; ok {
			if existing.Size != p.Size {
				return plan, errors.New("asset pointer size differs from remote index")
			}
			continue
		}
		plan.Missing = append(plan.Missing, oid)
		f, err := cache.Open(p)
		if err == nil {
			f.Close()
		} else {
			plan.Recover[oid] = true
		}
	}
	return plan, nil
}

// UploadAssets preserves the complete published index, including old versions.
// All required content must exist before the caller may publish new refs.
func (g Git) UploadAssets(ctx context.Context, store Store, next *Manifest, plan AssetPlan, recover func(context.Context, assets.Pointer) error) error {
	if len(plan.Pointers) == 0 {
		return nil
	}
	cache, err := g.AssetCache(ctx)
	if err != nil {
		return err
	}
	next.Assets = maps.Clone(next.Assets)
	if next.Assets == nil {
		next.Assets = map[string]Asset{}
	}
	for _, oid := range plan.Missing {
		p := plan.Pointers[oid]
		if plan.Recover[oid] {
			if recover == nil {
				return fmt.Errorf("asset %s is missing from the local cache", oid)
			}
			if err := recover(ctx, p); err != nil {
				return fmt.Errorf("retrieve asset %s before push: %w", oid, err)
			}
		}
		f, err := cache.Open(p)
		if err != nil {
			return fmt.Errorf("asset %s missing or corrupt; restore its content and git add it again: %w", oid, err)
		}
		id, err := store.Upload(ctx, "asset-sha256-"+oid, f, p.Size)
		f.Close()
		if err != nil {
			return fmt.Errorf("upload asset %s: %w", oid, err)
		}
		next.Assets[oid] = Asset{ID: id, Size: p.Size}
	}
	// Older helpers reject v2 rather than silently dropping required asset data.
	next.Version = 2
	return nil
}

// RememberAssetRemote records only validated Drive roots, never credentials or
// executable configuration. It also supports fetches made with a literal URL.
func (g Git) RememberAssetRemote(ctx context.Context, m *Manifest) error {
	if len(m.Assets) == 0 {
		return nil
	}
	if !ValidID(m.Root) {
		return errors.New("invalid asset repository root")
	}
	cache, err := g.AssetCache(ctx)
	if err != nil {
		return err
	}
	dir := filepath.Join(cache.Dir, "remotes")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, m.Root), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (g Git) AssetRemoteURLs(ctx context.Context) ([]string, error) {
	names, err := g.output(ctx, nil, "remote")
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, name := range strings.Fields(names) {
		url, err := g.output(ctx, nil, "remote", "get-url", "--", name)
		if err != nil {
			return nil, err
		}
		urls = append(urls, url)
	}
	return urls, nil
}
