// Package remotehelper implements Git's line-oriented fetch/push helper protocol.
package remotehelper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"strconv"
	"strings"

	"github.com/jr-dragon/git-remote-gdrive/internal/gdriveassets"
	"github.com/jr-dragon/git-remote-gdrive/internal/progress"
	"github.com/jr-dragon/git-remote-gdrive/internal/repository"
)

func FolderID(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "gdrive" || !repository.ValidID(u.Host) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("expected gdrive://{folder_id} without a path, query, or fragment")
	}
	return u.Host, nil
}

type Helper struct {
	OpenStore      func(context.Context) (repository.Store, error)
	Diagnostics    io.Writer
	Git            repository.Git
	OpenAssetStore gdriveassets.OpenStore
	store          repository.Store
	manifest       *repository.Manifest
	version        string
	dryRun         bool
	reporter       *progress.Reporter
}

func (h *Helper) load(ctx context.Context) error {
	if h.manifest != nil {
		return nil
	}
	if h.store == nil {
		var err error
		h.store, err = h.OpenStore(ctx)
		if err != nil {
			return err
		}
	}
	m, v, err := h.store.Load(ctx)
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	h.manifest, h.version = m, v
	return nil
}

func (h *Helper) git(ctx context.Context) (repository.Git, error) {
	if h.Git.Dir == "" {
		dir, err := repository.DiscoverGitDir(ctx)
		if err != nil {
			return h.Git, err
		}
		h.Git.Dir = dir
	}
	return h.Git, nil
}

func (h *Helper) Run(ctx context.Context, input io.Reader, output io.Writer) error {
	ctx, h.reporter = progress.New(ctx, h.Diagnostics)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	out := bufio.NewWriter(output)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			return nil
		}
		switch {
		case line == "capabilities":
			fmt.Fprint(out, "fetch\npush\noption\n\n")
		case line == "list" || line == "list for-push":
			if err := h.load(ctx); err != nil {
				return err
			}
			m := h.manifest
			if m.Refs[m.HEAD] != "" {
				fmt.Fprintf(out, "@%s HEAD\n", m.HEAD)
			}
			for _, ref := range repository.SortedRefs(m.Refs) {
				fmt.Fprintf(out, "%s %s\n", m.Refs[ref], ref)
				if oid := m.Peeled[ref]; oid != "" && line == "list" {
					fmt.Fprintf(out, "%s %s^{}\n", oid, ref)
				}
			}
			fmt.Fprintln(out)
		case strings.HasPrefix(line, "option "):
			fmt.Fprintln(out, h.option(strings.TrimPrefix(line, "option ")))
		case strings.HasPrefix(line, "fetch ") || strings.HasPrefix(line, "push "):
			kind, _, _ := strings.Cut(line, " ")
			batch := []string{line}
			terminated := false
			for scanner.Scan() {
				next := scanner.Text()
				if next == "" {
					terminated = true
					break
				}
				if kind == "push" && strings.HasPrefix(next, "option ") {
					fmt.Fprintln(out, h.option(strings.TrimPrefix(next, "option ")))
					if err := out.Flush(); err != nil {
						return err
					}
					continue
				}
				if !strings.HasPrefix(next, kind+" ") {
					return errors.New("mixed remote-helper command batch")
				}
				batch = append(batch, next)
			}
			if !terminated {
				return errors.New("unterminated remote-helper command batch")
			}
			if kind == "fetch" {
				if err := h.fetch(ctx, batch); err != nil {
					return err
				}
			} else {
				h.push(ctx, batch, out)
			}
			fmt.Fprintln(out)
		default:
			return errors.New("unsupported remote-helper command")
		}
		if err := out.Flush(); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (h *Helper) option(value string) string {
	name, arg, ok := strings.Cut(value, " ")
	if !ok {
		return "error missing option value"
	}
	switch name {
	case "verbosity":
		verbosity, err := strconv.Atoi(arg)
		if err != nil || verbosity < 0 {
			return "error invalid verbosity"
		}
		h.reporter.SetVerbosity(verbosity)
		return "ok"
	case "progress", "atomic":
		if arg != "true" && arg != "false" {
			return "error expected true or false"
		}
		if name == "progress" {
			h.reporter.SetEnabled(arg == "true")
		}
		return "ok"
	case "dry-run":
		if arg != "true" && arg != "false" {
			return "error expected true or false"
		}
		h.dryRun = arg == "true"
		return "ok"
	default:
		return "unsupported"
	}
}

func (h *Helper) fetch(ctx context.Context, batch []string) error {
	progress.Step(ctx, "Fetching repository...")
	if err := h.load(ctx); err != nil {
		return err
	}
	for _, line := range batch {
		parts := strings.Fields(line)
		if len(parts) != 3 || !repository.ValidOID(parts[1]) {
			return errors.New("invalid fetch command")
		}
		ref := parts[2]
		if ref == "HEAD" {
			ref = h.manifest.HEAD
		}
		want := h.manifest.Refs[ref]
		if strings.HasSuffix(ref, "^{}") {
			want = h.manifest.Peeled[strings.TrimSuffix(ref, "^{}")]
		}
		if want != parts[1] {
			return errors.New("fetch requested an object not advertised by this repository snapshot")
		}
	}
	g, err := h.git(ctx)
	if err != nil {
		return err
	}
	missingPacks, err := g.MissingPacks(ctx, h.manifest.Packs)
	if err != nil {
		return err
	}
	ctx = progress.BeginTask(ctx, "Fetch", len(missingPacks))
	if err := g.Hydrate(ctx, h.store, h.manifest); err != nil {
		return err
	}
	if err := g.RememberAssetRemote(ctx, h.manifest); err != nil {
		return err
	}
	progress.Step(ctx, "Fetch complete")
	return nil
}

type update struct {
	src, dst string
	force    bool
}

func (h *Helper) push(ctx context.Context, batch []string, out io.Writer) {
	updates := make([]update, 0, len(batch))
	var parseErr error
	seen := map[string]bool{}
	for _, line := range batch {
		spec := strings.TrimPrefix(line, "push ")
		force := strings.HasPrefix(spec, "+")
		spec = strings.TrimPrefix(spec, "+")
		src, dst, ok := strings.Cut(spec, ":")
		if !ok || !repository.ValidRef(dst) || seen[dst] {
			parseErr = errors.New("invalid or duplicate push destination")
		}
		seen[dst] = true
		updates = append(updates, update{src, dst, force})
	}
	err := parseErr
	if err == nil {
		err = h.apply(ctx, updates)
	}
	for _, u := range updates {
		dst := u.dst
		if !repository.ValidRef(dst) {
			dst = "invalid-ref"
		}
		if err != nil {
			message := strings.Join(strings.Fields(err.Error()), " ")
			fmt.Fprintf(out, "error %s %s\n", dst, message)
		} else {
			fmt.Fprintf(out, "ok %s\n", dst)
		}
	}
}

func (h *Helper) apply(ctx context.Context, updates []update) error {
	if err := h.load(ctx); err != nil {
		return err
	}
	g, err := h.git(ctx)
	if err != nil {
		return err
	}
	ctx = progress.BeginTask(ctx, "Push", 0)
	progress.Step(ctx, "Preparing...")
	if err := g.Hydrate(ctx, h.store, h.manifest); err != nil {
		return err
	}
	next := *h.manifest
	next.Refs = make(map[string]string)
	next.Peeled = make(map[string]string)
	next.Packs = append([]repository.Pack(nil), h.manifest.Packs...)
	next.Archives = maps.Clone(h.manifest.Archives)
	if next.Archives == nil {
		next.Archives = make(map[string]repository.Archive)
	}
	for ref, oid := range h.manifest.Refs {
		next.Refs[ref] = oid
	}
	for ref, oid := range h.manifest.Peeled {
		next.Peeled[ref] = oid
	}
	for _, u := range updates {
		if u.src == "" {
			delete(next.Refs, u.dst)
			delete(next.Peeled, u.dst)
			delete(next.Archives, u.dst)
			continue
		}
		oid, err := g.Resolve(ctx, u.src)
		if err != nil {
			return err
		}
		kind, err := g.Type(ctx, oid)
		if err != nil {
			return err
		}
		if strings.HasPrefix(u.dst, "refs/heads/") && kind != "commit" {
			return errors.New("branch must point to a commit")
		}
		old := h.manifest.Refs[u.dst]
		if old != "" && old != oid && !u.force {
			if strings.HasPrefix(u.dst, "refs/tags/") {
				return errors.New("tag already exists; force required")
			}
			if kind != "commit" || !g.Ancestor(ctx, old, oid) {
				return errors.New("non-fast-forward; fetch and merge or explicitly force push")
			}
		}
		next.Refs[u.dst] = oid
		if archive, ok := next.Archives[u.dst]; ok && archive.OID != oid {
			// The newly published ref has no confirmed ZIP until the post-push
			// export succeeds. Keep the old file, but do not advertise it as current.
			delete(next.Archives, u.dst)
		}
		delete(next.Peeled, u.dst)
		if strings.HasPrefix(u.dst, "refs/tags/") && kind == "tag" {
			next.Peeled[u.dst] = g.Peel(ctx, oid)
		}
	}
	// Prefer the pushed local default branch on initialization; otherwise preserve
	// HEAD until its branch is deleted, then choose a remaining branch predictably.
	if next.Refs[next.HEAD] == "" || h.version == "" {
		localHEAD := g.Head(ctx)
		chosen := ""
		for _, u := range updates {
			if strings.HasPrefix(u.dst, "refs/heads/") && u.src != "" && (u.src == localHEAD || u.src == "HEAD") {
				chosen = u.dst
				break
			}
		}
		if chosen == "" {
			for _, ref := range repository.SortedRefs(next.Refs) {
				if strings.HasPrefix(ref, "refs/heads/") {
					chosen = ref
					break
				}
			}
		}
		if chosen != "" {
			next.HEAD = chosen
		}
	}
	// Validate names before any remote writes. The old pack set cannot validate a
	// first push yet, so connectivity and final manifest validation follow packing.
	for ref := range next.Refs {
		parts := strings.Split(ref, "/")
		for i := 2; i < len(parts); i++ {
			if _, exists := next.Refs[strings.Join(parts[:i], "/")]; exists {
				return errors.New("conflicting ref names")
			}
		}
	}
	if err := g.Connected(ctx, next.Refs); err != nil {
		return err
	}
	if h.dryRun {
		progress.SetRemaining(ctx, 0)
		progress.Step(ctx, "Dry run complete; no refs published")
		return nil
	}
	assetPlan, err := g.PlanAssets(ctx, &next)
	if err != nil {
		return err
	}
	full := len(next.Packs) >= repository.MaxPacks
	needsPack, err := g.NeedsPack(ctx, h.manifest, &next, full)
	if err != nil {
		return err
	}
	archiveFiles := archiveTransferCount(updates, &next)
	remainingFiles := assetPlan.TransferCount() + 1 + archiveFiles
	if needsPack {
		remainingFiles++
	}
	if archiveFiles > 0 {
		remainingFiles++
	}
	progress.SetRemaining(ctx, remainingFiles)
	progress.Step(ctx, fmt.Sprintf("Processing %d remaining files...", remainingFiles))
	resolver := gdriveassets.Resolver{Git: g, Open: h.OpenAssetStore}
	if err := g.UploadAssets(ctx, h.store, &next, assetPlan, resolver.Ensure); err != nil {
		return err
	}
	if len(next.Refs) == 0 {
		next.Packs = nil
	} else if needsPack {
		pack, err := g.MakePack(ctx, h.store, h.manifest, &next, full)
		if err != nil {
			return err
		}
		if full {
			next.Packs = nil
		}
		if pack != nil {
			next.Packs = append(next.Packs, *pack)
		}
	}
	progress.Step(ctx, "Publishing refs...")
	if err := h.store.Publish(ctx, h.version, &next); err != nil {
		return err
	}
	progress.Step(ctx, "Refs published")
	// Refs are committed. Export failures must not turn a successful push into
	// an error or trigger rollback. ZIP metadata gets its own conditional publish.
	h.manifest = nil
	h.completeArchives(ctx, g, updates, next.Refs)
	h.manifest = nil
	return nil
}

func archiveTransferCount(updates []update, next *repository.Manifest) int {
	total := 0
	for _, u := range updates {
		if u.src == "" || (!strings.HasPrefix(u.dst, "refs/heads/") && !strings.HasPrefix(u.dst, "refs/tags/")) {
			continue
		}
		archive, exists := next.Archives[u.dst]
		if !exists || archive.OID != next.Refs[u.dst] {
			total++
		}
	}
	return total
}

func (h *Helper) warnArchive(err error) {
	if h.Diagnostics != nil {
		fmt.Fprintf(h.Diagnostics, "git-remote-gdrive: warning: refs were updated, but ZIP export is incomplete: %s\n", strings.Join(strings.Fields(err.Error()), " "))
	}
}

func (h *Helper) completeArchives(ctx context.Context, g repository.Git, updates []update, pushedRefs map[string]string) {
	var refs []string
	for _, u := range updates {
		if u.src != "" && (strings.HasPrefix(u.dst, "refs/heads/") || strings.HasPrefix(u.dst, "refs/tags/")) {
			refs = append(refs, u.dst)
		}
	}
	if len(refs) == 0 {
		return
	}
	// Loading the published snapshot refreshes Store's version precondition. A
	// newer push may already exist; never restore its refs to our earlier values.
	if err := h.load(ctx); err != nil {
		h.warnArchive(err)
		return
	}
	current := h.manifest
	if current.Archives == nil {
		current.Archives = make(map[string]repository.Archive)
	}
	changed := false
	for _, ref := range refs {
		oid := pushedRefs[ref]
		if current.Refs[ref] != oid {
			h.warnArchive(fmt.Errorf("%s changed again; skipped outdated ZIP", ref))
			continue
		}
		if archive, ok := current.Archives[ref]; ok && archive.OID == oid {
			continue
		}
		archive, err := g.MakeArchive(ctx, h.store, ref, oid)
		if err != nil {
			h.warnArchive(err)
			continue
		}
		current.Archives[ref] = archive
		changed = true
	}
	if changed {
		progress.Step(ctx, "Saving ZIP metadata...")
		if err := h.store.Publish(ctx, h.version, current); err != nil {
			h.warnArchive(fmt.Errorf("save ZIP metadata: %w", err))
		}
	}
}
