# Drive repository format v1

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
The directory must be a direct child of the selected root. Manifest and pack
files must be direct children of that directory.

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
4. If uninitialized, create a candidate storage directory using a generated ID.
5. Upload the new pack, if any, and an immutable manifest with generated IDs.
6. Read the root again. If its current pointer differs from the expected version,
   reject the push. Preserve unrelated custom properties.
7. Patch both root pointers with `If-Match: <current root ETag>` using Drive v2
   metadata. HTTP 412 is a concurrent-write conflict; no unconditional fallback
   is allowed. Missing ETags reject the write.
8. Report success only after publication. If the response was lost, reread the
   pointer: observing this attempt's unique manifest ID confirms its publication.
   Otherwise report conflict/uncertain failure and instruct the user to fetch.

Concurrent initialization follows the same protocol. Two candidates may be
uploaded, but only one root update can succeed for the same ETag. Force push
relaxes ancestry checks, never the publication precondition. Uploads and old
manifests are immutable, so stale readers retain a consistent object/ref snapshot.

Resumable uploads use 8 MiB chunks (a multiple of 256 KiB), status probes after
interruptions, and server-reported acknowledged offsets. Expired sessions and
exhausted retries fail the push; a later invocation starts a fresh upload.
Retryable rate limits and server errors use exponential backoff plus jitter.
Retries are bounded, cancellation is honored, and HTTP clients have timeouts.

## References and verification boundary

- [Drive custom properties](https://developers.google.com/workspace/drive/api/guides/properties)
  explains shared properties and their size limits.
- [Drive v2 file metadata](https://developers.google.com/workspace/drive/api/reference/rest/v2/files)
  exposes the file ETag used for conditional publication.
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
