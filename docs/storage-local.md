# Portable local storage v1

`git-remote-gdrive-local` reuses the remote-helper protocol and repository logic
with a filesystem Store. No credentials, browser, or Google API is needed.
The root directory must already exist and should be outside the Git worktree.
Select it with an absolute URL:
`gdrive-local:///home/me/remote` or `gdrive-local:///C:/Users/me/remote` on Windows.
Quote spaces and percent-encode reserved URL characters. Relative paths, URL
authorities, queries, fragments, and UNC paths are rejected.

## Layout

```text
root/
  @.git-remote-gdrive/
    FORMAT                 # git-remote-gdrive-local 1, followed by newline
    CURRENT                # obj-<sha256> or unborn, followed by newline
    obj-<sha256>            # immutable pack, asset, or JSON manifest bytes
  branches/
    FORMAT                 # same local format marker
    main.zip
  tags/
    FORMAT
    v1.zip
```

The root may temporarily contain `.gdrive-local.lock/`; staging files start with
`.tmp-`. Copy only after push completes. A lock left by a killed writer must be
removed manually only after confirming no writer is active. No timeout breaks locks.

FORMAT is mandatory once the storage directory exists. CURRENT explicitly records
an unborn repository or one immutable manifest. Missing/invalid pointers fail;
readers never scan for the newest manifest or treat a partial copy as empty.
Reading a root without a storage directory is empty and creates nothing. Reserved
storage and archive directories without the correct marker are rejected.

Manifests use the shared v1/v2 schema, with constant logical bindings `root=local`
and `directory=storage`. Object IDs are `obj-` plus lowercase SHA-256 of their bytes.
No absolute local paths or Drive IDs are embedded. The manifest ID verifies its
JSON bytes. Readers require all indexed packs/assets to exist with declared sizes;
downloads verify content hashes and Git verifies/indexes packs. Missing data fails
with an instruction to finish copying/syncing.

The filename passed by repository logic (`pack-*.pack`, `manifest.json`, or
`asset-sha256-*`) is only a progress label. Immutable local objects are always
stored by content ID, so identical bytes are reused and portable manifests never
depend on descriptive filenames.

Archive directory IDs are `branches` and `tags` (the logical key remains `branch`).
ZIP IDs are a stable hash of their relative directory/name. ZIP names use the API
backend's percent-encoded ref naming. ZIPs are convenience exports, never used to
reconstruct Git history or assets.

Copy the entire root, including old immutable objects, to any new absolute path.
The receiving user configures that path in their remote URL. HEAD, branches, tags,
packs, and assets remain usable without translating identifiers.

## Publication and concurrency

All mutations take an exclusive directory lock on the same filesystem. Uploads
stream to temporary files while hashing, validate byte counts, flush/close, and
rename into place. Existing immutable objects are verified and reused without
overwriting. `os.Root` scopes filesystem access and prevents symlinks escaping
the selected root.

Push validates refs, hydrates old packs, and writes assets/packs before an immutable
manifest. Under the write lock it rereads CURRENT, compares the expected version,
checks required objects, then replaces CURRENT using a staged file and rename.
Failed/short/cancelled copies preserve the previous published file. Force push
does not bypass version checks. Old snapshots/objects remain; there is no GC.

After refs publish, ZIPs are replaced at fixed paths with a version check under
the lock. A second conditional manifest publication records exports without
restoring stale refs. ZIP failures warn without rolling back refs; ref deletion
retains its last ZIP. Progress, quiet, and dry-run reuse the shared helper.

These protections depend on local filesystem locking/rename behavior; they are
not a distributed transaction or a guarantee of power-loss durability. Cloud sync
between computers has no shared lock and can transfer CURRENT before objects.
Coordinate one writer at a time, wait for full sync before reading and after
pushing, and make files available offline. Preserve both complete snapshots on
sync conflicts, reconcile their Git histories, then publish a complete replacement.
Do not let conflict resolution arbitrarily choose CURRENT.

## Assets and migration

The optional filters work unchanged via `git gdrive install` and `.gitattributes`.
Local asset URLs selected by the user are remembered in the Git cache for
literal-URL fetches; downloaded manifests cannot supply filesystem paths.
Asset content is written into the same content-addressed object directory before
refs publish, and checkout retrieves it lazily through the local Store.

API repositories depend on Google-managed IDs and PUBLIC properties that ordinary
upload/download does not preserve. Uploading this folder does not enable
`gdrive://`, and downloading an API repository does not convert it. Migrate from
a Git checkout with the desired history/assets available: add a fresh destination
remote and push desired branches/tags. No automatic format conversion occurs.
