# Drive repository formats v1 and v2

This describes the API backend. The portable `gdrive-local://` backend is specified
in [local storage](storage-local.md).

`drive.Store` depends on the `drive.API` interface. The HTTP client and
the official Drive v2/v3 SDK adapters implement it and share this storage contract.
Select with `GIT_GDRIVE_API_BACKEND=http|sdkv2|sdkv3` (default: `sdkv3`);
see [backend comparison](drive-backends.md).

## Discovery and identity

The URL is exactly `gdrive://<folder-id>`. The selected folder is the repository
root; unrelated files in that folder are untouched. A read of an uninitialized
folder returns an empty repository and creates no remote files.

Two PUBLIC custom properties on the root define the current repository:

| Property | Value |
| --- | --- |
| `gdrive-repo` | Drive ID of the canonical storage directory |
| `gdrive-manifest` | Drive ID of the current immutable manifest |

The directory is named `@.git-remote-gdrive` and has PUBLIC property
`gdrive-format=1`. PUBLIC properties are shared across OAuth applications that
have permission to access the file; they do not grant access to Drive contents.
The directory must be a direct child of the selected root. Manifest, pack, and
v2 asset files must be direct children of that directory.
The `gdrive-format=1` property identifies the directory layout; the manifest's
`version` controls required reader/writer features.

The default `sdkv3` adapter maps these shared properties to Drive v3
`properties`; it maps PRIVATE properties to `appProperties`. The v2 adapters use
the v2 property list and preserve visibility explicitly. All adapters expose the
same project-owned metadata types to `drive.Store`, so switching backends does
not migrate or rewrite repository data.

Optional `archive_directories` in the manifest maps `branch` and `tags` to their
Drive folder IDs. The `branch` key is retained for compatibility and identifies
the folder named `branches`; the `tags` key identifies `tags`.
These named folders are direct children of the selected root,
separate from `@.git-remote-gdrive`, and carry PUBLIC property `gdrive-format=1`.
They are created lazily and identified by ID on later pushes. Existing unrelated
folders with those names are not adopted. Concurrent/failed first archive uploads
can leave candidate folders; the successfully published manifest selects the
canonical IDs just as it does for repository initialization.

On the next branch ZIP export after refs commit, a legacy canonical folder named
`branch` is renamed in place to `branches`, preserving all folder/file IDs. The
helper validates its parent, format marker, and current root manifest, and sends
the folder's ETag with the conditional rename. Missing ETags or failed renames
fail that export with a warning. Dry runs and rejected ref updates never rename
the folder. Unrelated same-name folders are not adopted. Older helpers that
require the singular folder name must be updated for ZIP writes.

Drive file names are not unique. Multiple candidate directories can exist after
concurrent initialization, and multiple `manifest.json` snapshots normally exist.
The root pointers select exactly one repository and one current manifest. A client
must never fall back to a name search when pointers are malformed or files are
missing: doing so could select unrelated or unpublished data.

## Manifest schema

```json
{
  "format": "git-remote-gdrive",
  "version": 1,
  "object_format": "sha1",
  "root": "ROOT_FOLDER_ID",
  "directory": "STORAGE_DIRECTORY_ID",
  "head": "refs/heads/main",
  "refs": {
    "refs/heads/main": "0123456789abcdef0123456789abcdef01234567",
    "refs/tags/v1": "abcdef0123456789abcdef0123456789abcdef01"
  },
  "peeled": {
    "refs/tags/v1": "0123456789abcdef0123456789abcdef01234567"
  },
  "archive_directories": {
    "branch": "BRANCH_DIRECTORY_ID",
    "tags": "TAG_DIRECTORY_ID"
  },
  "archives": {
    "refs/heads/main": {
      "id": "ZIP_FILE_ID",
      "oid": "0123456789abcdef0123456789abcdef01234567",
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "size": 2048
    }
  },
  "packs": [
    {
      "id": "PACK_FILE_ID",
      "git_hash": "0123456789abcdef0123456789abcdef01234567",
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "size": 4096
    }
  ]
}
```

Hashes and sizes above are illustrative. `git_hash` is the pack trailer hash;
`sha256` covers the entire pack including its trailer. `refs` maps full ref names
to object IDs. `peeled` contains the fully dereferenced object ID of annotated
tags. `head` is always a branch ref name; it can be unborn if no branches exist.
List advertises HEAD only when its target exists. Unknown format versions and
invalid refs, pack descriptors, or repository bindings fail before import.

`archives` and `archive_directories` are optional additions to v1. Older manifests
remain readable without them. `archives` maps a full branch/tag ref to its current
ZIP ID, ref object ID, size, and digest. Its `oid` must equal the corresponding
entry in `refs`; directory IDs must be distinct from the repository root, storage
directory, and each other. Legacy writers do not preserve these fields, so use
updated helpers for pushes once ZIP snapshots are needed.

Manifest v2 adds a required-to-preserve `assets` index, mapping SHA-256 digests to
`{"id":"DRIVE_FILE_ID","size":123}` descriptors. A first push containing asset
pointers upgrades v1 to v2. v1 cannot contain a nonempty asset index, and a v2
repository never automatically downgrades. Older helpers reject v2 instead of
silently losing required data. Descriptors are retained across ref deletion and
force pushes. See [gdrive-assets](assets.md) for the pointer and cache format.

## Branch and tag ZIP snapshots

Only after successfully publishing the push's refs, generate a ZIP on local
temporary disk for each non-deletion branch/tag update using its exact published
object ID. Branches and tree/commit tags use
`git archive --format=zip <oid>`; annotated tags are dereferenced by Git. Tags
pointing to blobs use a ZIP containing one `blob` entry. The working tree is not
archived. Standard Git archive attributes apply, and submodule contents are not
recursively downloaded. The temporary file is removed on completion or failure.

ZIPs have MIME type `application/zip`. Names are
`<percent-encoded-short-ref>.zip`, stored under `branches/` for
`refs/heads/*` and `tags/` for `refs/tags/*`. Encoding `/` and `%` distinguishes
`feature/a`, `feature%2Fa`, and `feature-a`. The full destination ref, not a local
source branch name, determines the location. Other ref namespaces have no ZIP.
Search the canonical folder for a non-trashed file with that exact name, including
all result pages. If it exists, use Drive `files.update` with its ID to overwrite
its content through a resumable upload; otherwise create a file. Do not delete and
recreate matching files. Incomplete searches and duplicate matching names fail
instead of choosing an arbitrary file. A same-name folder or native Google
document is not a writable ZIP target.

Uploads reuse the resumable transport. Ref publication removes stale ZIP mappings
for changed/deleted refs; unchanged mappings are preserved. After successful ZIP
uploads, a second conditional manifest publication records their file IDs, object
IDs, digests, and directories without changing the newly published refs. Dry-run
performs no ZIP uploads. An existing mapping for the same ref/object ID can be reused.
An up-to-date Git push may send no update commands, so it does not backfill ZIPs.
Deletion removes the manifest mapping without deleting the last ZIP. Recreating
the ref therefore finds and overwrites the existing file even without a manifest
entry. Files produced by the earlier versioned naming scheme are not deleted;
the next update creates/overwrites the fixed filename.

A ZIP adds a complete file snapshot per updated ref, independently of pack
incrementality/compaction. Updates keep the same file ID rather than accumulating
new ZIP files for every commit. Drive may retain file revisions according to its
normal retention policy; the helper does not pin them.

ZIPs are mutable convenience exports. Once refs have committed, ZIP generation,
upload, or metadata failures emit warnings on stderr while the helper still reports
`ok` for the successful Git ref updates. No rollback is attempted. Other ZIPs in
the batch may still succeed. Failed ref publication does not start ZIP export.
An up-to-date push does not automatically repair a failed export; a later update
of the ref triggers another attempt.

The export phase reloads the latest manifest and skips any target ref whose object
ID has already changed again. Before overwriting, it checks the root manifest
version and passes the ZIP ETag when initiating the update; a missing ETag fails
the export. ZIP metadata is saved with a separate CAS, preserving the refs from
the reloaded snapshot. Concurrent changes cause a warning, never restoration of
older refs. An upload already in progress can still race a later push, since this
is not a multi-file transaction. A missing archive entry means no confirmed export
for the current ref; a recorded digest can detect changed ZIP bytes. Packs and
manifests remain authoritative for Git clone/fetch regardless of export failures.

## Git transport

The helper advertises `fetch`, `push`, and `option`. It implements `list`,
`list for-push`, batched `fetch`, and batched `push`, using stdin/stdout framing
from the [Git remote-helper protocol](https://git-scm.com/docs/gitremote-helpers).
Diagnostics go to stderr. Options include verbosity, progress, dry-run, and
atomic; unsupported options receive `unsupported`.

A list pins an immutable manifest for subsequent requests in that helper session.
Fetching imports packs in manifest order and checks object connectivity. A ref
request must match that snapshot's advertised object ID. Pushes validate all
requested updates as one batch, including commit-only branch tips, non-fast-forward
updates, tag replacement, ref namespace collisions, and source connectivity.

## Pack strategy

Git itself produces compressed packs with `pack-objects --stdout --revs`.
Previous remote tips are exclusions for incremental packs. No `--thin` pack is
produced: each pack contains its delta bases, although historical objects can
reside in earlier packs. The reader imports the active pack chain in order.

At most 16 packs are active. Once that threshold is reached, the next push packs
all objects reachable from the resulting refs and starts a new chain. Ref-only
updates can reuse existing packs; deleting every ref produces an empty chain.
Pack creation is local and streams to a temporary file. Pack downloads stream to
disk, verify size and SHA-256, check the Git trailer, and use `index-pack --strict`.
Git's existing `.pack`/`.idx` pairs act as the local pack cache, with connectivity
verified before reporting fetch success.

The active chain bounds API calls independently of loose object count. It does
not bound total Drive storage: old snapshots and their objects remain available
to readers that listed before a newer push. Garbage collection requires a separate
reader-retention/recovery design and is deliberately not performed by pushes.

## Atomic publication and failure handling

1. Read the root pointer and save the manifest ID as the expected version.
2. Load the immutable manifest and hydrate its packs locally as needed.
3. Validate the entire push batch. Dry-run stops before remote writes.
4. Remove stale archive mappings for changed/deleted refs, preserving unchanged
   mappings. Scan reachable history for gdrive-assets pointers and upload missing
   immutable asset objects. Update the asset index and use manifest v2 when
   assets are present. Asset failures stop the push before publication. If
   uninitialized, uploads create a candidate storage directory.
5. Upload the new pack, if any, and the immutable ref manifest with generated IDs.
6. Read the root again. If its current pointer differs from the expected version,
   reject the push. Preserve unrelated custom properties.
7. Patch both root pointers with `If-Match: <current root ETag>` using the selected
   Drive API backend. HTTP 412 is a concurrent-write conflict; no unconditional fallback
   is allowed. Missing ETags reject the write.
8. Confirm ref publication. If the response was lost, reread the
   pointer: observing this attempt's unique manifest ID confirms its publication.
   Otherwise report conflict/uncertain failure and instruct the user to fetch.
9. Reload current refs and generate/upload ZIPs for still-current pushed targets,
   creating candidate archive directories if needed. Record IDs, hashes, and sizes.
10. Conditionally publish a new manifest for successful ZIP metadata, without
    restoring or changing refs. Export or metadata failures warn on stderr; the
    already-committed ref updates are still reported as successful to Git.

Concurrent initialization follows the same protocol. Two candidates may be
uploaded, but only one root update can succeed for the same ETag. Force push
relaxes ancestry checks, never the publication precondition. Pack uploads and old
manifests are immutable, so stale Git readers retain a consistent object/ref snapshot;
ZIP convenience exports are overwritten independently as described above.

The HTTP backend's resumable uploads use 8 MiB chunks (a multiple of 256 KiB), status probes after
interruptions, and server-reported acknowledged offsets. Expired sessions and
exhausted retries fail a pack/ref operation before commit, or warn for ZIP work
after commit. A later ref update starts a fresh failed-export attempt.
Both SDK backends use multipart below 8 MiB and native SDK resumable uploads at or
above that size. Their chunk retry policy differs; see the backend comparison.
Retryable rate limits and server errors use exponential backoff plus jitter.
Retries are bounded, cancellation is honored, and HTTP clients have timeouts.

All creates use a generated file ID before uploading. This keeps whole-operation
retries idempotent: a lost successful response cannot silently create a second
pack, manifest, asset, directory, or ZIP. SDK uploads that still return an error
verify the generated/existing ID, parent, name, MIME type, size, and SHA-256 before
accepting a lost response as success.

## References and verification boundary

- [Drive custom properties](https://developers.google.com/workspace/drive/api/guides/properties)
  explains shared properties and their size limits.
- [Drive v2 file metadata](https://developers.google.com/workspace/drive/api/reference/rest/v2/files)
  exposes the JSON file ETag used by v2 backends. The v3 adapter reads the
  file GET response ETag header instead and rejects conditional writes if absent.
- [Drive resumable uploads](https://developers.google.com/workspace/drive/api/guides/manage-uploads)
  defines chunks, status probes, and interrupted upload handling.
- [Drive error handling](https://developers.google.com/workspace/drive/api/guides/handle-errors)
  describes quota errors and exponential backoff.
- [Generated file IDs](https://developers.google.com/workspace/drive/api/guides/create-file)
  permit safe retries without duplicate file creation.

Automated tests run actual Git commands against an HTTP mock that enforces ETags,
returns rate-limit errors, and interrupts uploads. Before relying on this format
for production data, validate conditional root-property updates, permissions, and
resumable uploads against a real Drive account. No live verification is claimed.
