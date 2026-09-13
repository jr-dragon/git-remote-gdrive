# Optional gdrive-assets

These filters also work with `gdrive-local://` remotes without OAuth. Local asset
objects travel with the complete remote folder and are verified during checkout.
See [local storage](storage-local.md) for portability and sync requirements.

`git gdrive install` registers clean/smudge filters in the current repository's
Git config. `--global` registers them in user config, including before cloning.
Installation needs neither credentials nor a network connection and does not
modify `.gitattributes`, the index, or history. Repeated installation is safe;
conflicting custom filter settings are rejected for the user to review.

Select paths through normal Git attributes:

```gitattributes
*.bin filter=gdrive-assets diff=gdrive-assets merge=gdrive-assets -text
```

Git handles nested attributes, quoted patterns, and overrides. `-text` prevents
line-ending conversion; the diff/merge drivers treat these files as binary and
leave conflicting versions for explicit resolution. Ignored files still require
explicit addition. A bare custom `gdrive-assets` attribute alone does not enable
the filter. All participants must configure the filters themselves; Git does not
copy filter commands from a repository's versioned files.

## Pointer and local cache

The canonical pointer is UTF-8 with LF endings and a final newline:

```text
version https://github.com/jr-dragon/git-remote-gdrive/spec/gdrive-assets/v1
oid sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
size 123
```

The digest covers the exact asset bytes. Size is a canonical nonnegative decimal
integer less than `2^63-1`. Digests are lowercase hexadecimal. Unknown versions
and malformed pointers using this reserved format fail filtering. This is a
project-specific format, not a Git LFS pointer or LFS server implementation.

Clean reads stdin, streams content to a private temporary cache file, computes
SHA-256, and atomically installs the object before emitting its pointer. Cleaning
an existing pointer preserves it, even when content is not locally available;
push checks availability. No network access or browser authentication occurs
during clean.

Objects live at `<git-common-dir>/gdrive-assets/objects/<first-two-hex>/<sha256>`.
Linked worktrees share this cache. Git's own blob and pack stores contain only
pointers for filtered versions. Existing commits with full binary blobs remain
unchanged. Use `git add --renormalize -- <path>` to convert currently tracked
files after configuring attributes.

Cache reads verify both size and hash before returning bytes. Downloads write to
temporary files and must match the pointer before installation. Failed downloads
cannot install partial or unverified content. Temporary files and directories use
restrictive permissions where supported.

## Push and remote storage

Push examines blobs reachable from all resulting refs, including historical
commits and tags pointing directly to blobs. It uses Git batch object inspection
and only reads small candidate pointer blobs, without loading full ordinary
binaries. It recognizes the reserved pointer format regardless of current
attributes, preserving assets from deleted or renamed historical paths.

The manifest v2 `assets` index maps each SHA-256 to a Store object ID and size.
For `gdrive://`, missing assets are uploaded sequentially as immutable
`asset-sha256-<digest>` files directly within the canonical
`@.git-remote-gdrive` directory. Discovery uses manifest IDs, never a filename
search, and transfers use the selected Drive backend's upload/retry behavior.
For `gdrive-local://`, the same bytes are stored as immutable `obj-<sha256>`
objects in the portable storage directory. Existing indexed digests are reused;
different paths and versions with identical bytes share one object. Empty assets
are supported. Many unique tiny assets still cost one remote object each; use
ordinary Git storage for those files, where pack files aggregate them efficiently.

When a missing local object is needed for a new destination, the helper attempts
to recover it from configured `gdrive://` and `gdrive-local://` fetch URLs, plus
Drive roots or local URLs remembered by previous fetches. Only user-selected
remote URLs can introduce local filesystem paths. Remote manifests are reused
within that transfer batch.
All required uploads must finish before pack/manifest publication can update
refs. Failures leave published refs unchanged; already uploaded candidates may
become orphans. Dry-run performs no asset uploads. The conditional manifest
publication resolves concurrent writers as it does for packs.

Once assets are used, the manifest becomes v2 and stays v2. Retain the full asset
index across force pushes, ref deletion, and ZIP metadata updates. Older helpers
reject this version. No asset garbage collection is performed locally or on Drive.
The existing 8 MiB manifest limit also bounds this index; a larger manifest causes
publication to fail, rather than dropping entries.

## Fetch, clone, and checkout

Fetch transfers Git packs and remembers the validated remote source locally; it
does not eagerly download all asset history. Smudge restores a pointer from
verified cache content or downloads it through the matching Drive or local Store.
Remote lookup uses configured fetch URLs and remembered Drive roots/local URLs,
never locations supplied inside pointers or downloaded manifests. Different
OAuth clients can resolve the same public Drive repository metadata using their
own authorized access.

Install globally before clone, or clone with `--no-checkout`, install locally,
then run `git checkout HEAD -- .`. Without installed filters, Git leaves pointers
in the working tree. With `required=true`, missing/corrupt assets cause checkout
to fail; `GIT_GDRIVE_SKIP_SMUDGE=1` explicitly requests pointer-only checkout.
Cached assets can be checked out offline. Uncached historical versions require
access to a remote that retains their content.

ZIP convenience exports follow the ref-success-first sequence and contain Git's
committed pointers; `git archive` does not invoke smudge. Assets themselves are
required repository content, so their uploads precede refs. ZIP export failure
continues to warn without rolling back a successful push.

Both `gdrive://` and `gdrive-local://` pushes transfer required assets through
their selected Store before refs are published. Pushing pointer commits to other
Git transports does not upload asset content. This implementation provides no
LFS HTTP server, history migration command, automatic build step, or Git LFS-style
asset locking.
