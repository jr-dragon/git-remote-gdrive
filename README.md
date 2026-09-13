# git-remote-gdrive

[English](README.md) | [繁體中文](README-zh.md)

[![Testing](https://github.com/jr-dragon/git-remote-gdrive/actions/workflows/testing.yml/badge.svg)](https://github.com/jr-dragon/git-remote-gdrive/actions/workflows/testing.yml)

Use Google Drive as a Git remote through `gdrive://{folder_id}`. The project
provides browser OAuth authentication and a remote helper for clone, fetch, and push.
Use `gdrive-local:///absolute/path` for a folder remote without OAuth or Drive API
access, then transfer that folder yourself or with Drive for desktop.

Detailed specifications:

- [Drive storage and publication](docs/storage.md)
- [Portable local storage](docs/storage-local.md)
- [Optional gdrive-assets](docs/assets.md)
- [Drive backend comparison](docs/drive-backends.md)

## Install

With the Go version specified in `go.mod`:

```sh
go install ./git-gdrive ./git-remote-gdrive ./git-remote-gdrive-local
```

Add your Go binary installation directory to `PATH`. Alternatively, run `make build`
and add the resulting `build/` directory to `PATH`. Git 2.36 or later is required.
`git-gdrive` supplies `git gdrive`; Git invokes `git-remote-gdrive` for `gdrive://`
and `git-remote-gdrive-local` for `gdrive-local://`. Local mode needs no credentials.

## Local folder remote (no Drive API)

Create a dedicated folder outside your Git working tree and use its absolute path:

```sh
mkdir -p /absolute/path/Drive/project-remote
git remote add origin 'gdrive-local:///absolute/path/Drive/project-remote'
git push -u origin main
git push origin --tags
```

Windows uses `gdrive-local:///C:/Users/me/Drive/project-remote`. Quote URLs with
spaces; encode literal `%`, `#`, and `?` as `%25`, `%23`, and `%3F`. Relative and
UNC paths are unsupported. Use `git remote set-url origin <local-url>` to change
an existing remote.

After push finishes, upload/copy the **entire root**, including
`@.git-remote-gdrive/`. Another user downloads the complete folder and runs:

```sh
git clone 'gdrive-local:///absolute/path/downloaded/project-remote' project
cd project
# Develop and commit, then:
git push origin main
```

The portable folder includes packs, refs/HEAD/tags, manifests, and optional assets.
ZIPs are written to `branches/` and `tags/` after refs commit. Assets use the same
optional `git gdrive install` and `.gitattributes` setup. For asset checkout during
clone, install filters with `git gdrive install --global` beforehand, or clone with
`--no-checkout`, install locally, then check out the branch.

Coordinate **one writer at a time across computers**. Finish downloading before
fetch/pull; finish uploading after push before handing the folder to the next
writer. Make the folder available offline with Drive for desktop. Locks and version
checks protect processes sharing the same local filesystem, not independently
synced copies. If `CURRENT` has a sync conflict, preserve both copies and reconcile
their Git histories before publishing a complete replacement. Missing pointers or
objects cause errors; the helper never selects an older manifest automatically.

This format differs from the API backend's Drive IDs/custom properties. Uploading
a local remote does **not** make it readable via `gdrive://`, and downloading an
API remote does not convert it. Migrate from a Git checkout with the required
history/assets available: add a fresh destination remote and push the desired
branches and tags. See [local storage format](docs/storage-local.md).

## Choose the Drive API backend

All three implementations share credentials, repository format, and ref
publication rules. Select one for a command or export the variable for
subsequent Git commands (including asset filters):

```sh
GIT_GDRIVE_API_BACKEND=http git clone gdrive://FOLDER_ID project-http
GIT_GDRIVE_API_BACKEND=sdkv2 git clone gdrive://FOLDER_ID project-sdkv2
GIT_GDRIVE_API_BACKEND=sdkv3 git clone gdrive://FOLDER_ID project-sdkv3
GIT_GDRIVE_API_BACKEND=sdkv3 git push origin main
```

`sdkv3` is the default when `GIT_GDRIVE_API_BACKEND` is unset or empty.
`http` uses the existing Drive v2 HTTP implementation.
`sdkv2` uses `google.golang.org/api/drive/v2`; `sdkv3` uses
`google.golang.org/api/drive/v3`. Both SDKs use native media uploads. The old
`sdk` value has been renamed to `sdkv2` and is no longer accepted. Unknown
values are errors; there is no automatic fallback. On PowerShell use
`$env:GIT_GDRIVE_API_BACKEND = 'sdkv3'` before running Git. This choice does not
affect local-folder operations or grant additional Workspace permissions.

Run `make benchmark-drive` for a repeatable local comparison. See
[backend comparison](docs/drive-backends.md) for architecture, upload/retry
differences, measurement limits, and commands to compare real repositories.

## Google OAuth setup

1. Create a Google Cloud project and enable the Google Drive API.
2. Configure the OAuth consent screen. If the app is in testing, add your Google
   account as a test user.
3. Create an OAuth client with application type **Desktop app** and download its
   client JSON. Keep this file outside the repository.
4. Run:

   ```sh
   git-gdrive config --client-file /path/to/client_secret.json
   ```

Alternatively, set `GIT_GDRIVE_CLIENT_FILE` to that path and run `git gdrive config`.
The command opens your default browser, requests offline access, and waits up to
five minutes for authorization. Press Ctrl+C to cancel.

The requested scope is `https://www.googleapis.com/auth/drive`, which permits
reading and managing all Drive files. This supports the workflow of
accessing an existing folder directly by ID. The narrower `drive.file` scope
requires files to be created by or explicitly shared with the app, such as through
Google Picker; entering a folder ID alone does not grant that access.
See [Google's Drive scope documentation](https://developers.google.com/workspace/drive/api/guides/api-specific-auth).

On success, the command atomically writes JSON credentials to:

```text
~/.config/git-remote-drive/credential
```

The file contains the OAuth client ID and secret, access token, refresh token,
token type, and expiry. On Unix, the directory uses mode `0700` and the file uses
`0600`. Failed authorization leaves any previous credential unchanged. Credentials
are sensitive and stored as plaintext; keep them out of source control.

## Manual callback fallback

Normally, a temporary HTTP listener bound to `127.0.0.1` receives Google's redirect.
If a listener cannot be started, the command automatically switches to manual
input. You can also force this mode:

```sh
git-gdrive config --client-file /path/to/client_secret.json --manual
```

If the browser does not open, open the printed authorization URL yourself.
After granting permission, the browser may display a connection error for the
loopback address (manual mode uses `http://127.0.0.1:1/oauth2/callback`). Copy the
**entire final URL from the address bar**, paste it into the terminal, and press
Enter. This also works when the browser runs on a different machine from the CLI.
The URL must belong to the current authorization attempt; a bare code or access
token is not accepted. Both automatic and manual flows validate state and use PKCE.

This uses the Desktop app loopback redirect, not Google's discontinued OOB flow.
See [Google's native app OAuth documentation](https://developers.google.com/identity/protocols/oauth2/native-app).

## Project layout and checks

- `git-gdrive/main.go`: command parsing and configuration workflow.
- `git-remote-gdrive/main.go`: remote-helper entry point.
- `internal/remotehelper`: Git protocol, ref discovery, and push validation.
- `internal/repository`: versioned manifests, pack generation, and object verification.
- `internal/assets`: asset pointers and verified content cache.
- `internal/gdriveassets`: optional filter installation, clean/smudge commands, and asset retrieval.
- `internal/drive`: Drive storage, conditional publication, retries, and resumable uploads.
- `internal/googleauth`: OAuth flow, callback validation, and credential persistence.
- `internal/browser`: default browser launchers for macOS, Linux, and Windows.

```sh
go test -race ./...
go vet ./...
```

Tests use local mock OAuth/Drive endpoints, filesystem remotes, and temporary
repositories. Integration tests execute real Git commands against every Drive API
backend and the local backend, covering push, clone, fetch, assets, ZIP exports,
concurrent initialization, corruption, interrupted upload, backend interoperability,
and compaction. They do not contact Google or use real credentials.

## Use a Drive repository

Create a Google Drive folder and copy its folder ID from the browser URL. Complete
`git gdrive config` first, then push an existing repository:

```sh
git remote add origin gdrive://YOUR_FOLDER_ID
git push -u origin HEAD
git push origin --tags
```

The first successful push initializes storage inside the folder. Other users with
access to the same folder can authenticate with their own Google accounts and run:

```sh
git clone gdrive://YOUR_FOLDER_ID
git fetch origin
git push origin HEAD
```

Readers need access to the folder and its contents. Writers also need permission
to create files and update the root folder's metadata. Sharing is managed in
Google Drive; the helper does not change permissions. Each user can use their own
Desktop OAuth client, since repository discovery uses public Drive properties
(visible to authorized applications, not publicly accessible file contents).

The helper supports branches, lightweight and annotated tags, ref deletion,
explicit force pushes, dry runs, and atomic push batches. It rejects divergent
branch updates and existing tag replacements unless force is requested. The first
push chooses the pushed local default branch as remote HEAD when possible;
otherwise it chooses the first branch in sorted order. HEAD remains stable until
that branch is deleted, then moves to a remaining branch if one exists.

## Transfer progress

Clone, push, pull, and fetch group all files into one remote-helper task and show
its overall percentage, current `file N/total`, current file percentage, and byte
counts on stderr when Git enables progress. Push plans pack, asset, ZIP, and
manifest transfers before its uploads; fetch groups all missing pack downloads.
If push must first retrieve old packs needed for validation, those appear in the
same task before its final total is known. Updates are limited to once per second
per file, plus start/end messages. Retries use absolute byte offsets, failed files
do not advance the overall completion count, and 100% is shown only when every
planned file succeeds. Ref publication is reported separately after the
conditional update.

Git normally enables progress on an interactive terminal. To force progress when
output is redirected, use:

```sh
git clone --progress gdrive://YOUR_FOLDER_ID
git push --progress origin HEAD
git pull --progress
```

`--quiet` and `--no-progress` suppress remote-helper progress; errors and ZIP
export warnings are still reported. Git controls its own checkout/merge output
after the remote-helper phase.

## Optional large assets

Use `gdrive-assets` to version binaries or libraries with small Git pointers and
store their actual content in the selected `gdrive://` or `gdrive-local://`
remote. This is optional; ordinary repositories need no filter setup. It follows
Git's [clean/smudge filter mechanism](https://git-scm.com/docs/gitattributes),
with its own pointer and storage format, and does not require Git LFS.

Inside the repository, install the filters:

```sh
git gdrive install
```

Add patterns to `.gitattributes` and commit that file with your assets:

```gitattributes
*.so  filter=gdrive-assets diff=gdrive-assets merge=gdrive-assets -text
*.dll filter=gdrive-assets diff=gdrive-assets merge=gdrive-assets -text
dist/** filter=gdrive-assets diff=gdrive-assets merge=gdrive-assets -text
```

```sh
git add .gitattributes dist/
git commit -m "Track release assets"
git push origin HEAD
```

`.gitignore` still applies: explicitly use `git add -f` for an ignored asset that
you intend to version. For files already tracked before filter setup, use
`git add --renormalize -- path/to/asset` and commit the conversion. Existing
history is preserved and is not rewritten.

`git add` caches content locally and stages a SHA-256/size pointer. Push uploads
missing assets, including versions needed by historical commits, before publishing
refs. Identical bytes reuse one remote object within the repository. Missing or
failed asset uploads fail the push without updating refs. The usual ZIP export
still runs after refs commit; ZIP entries contain the committed pointers.

Each collaborator must install filters on their own machine. To enable them for
matching paths across repositories before cloning:

```sh
git gdrive install --global
git clone gdrive://YOUR_FOLDER_ID
```

Or keep installation local to the new clone:

```sh
git clone --no-checkout gdrive://YOUR_FOLDER_ID repo
cd repo
git gdrive install
git checkout HEAD -- .
```

Checkout downloads assets on demand and verifies their size and SHA-256. Cached
content works offline. Without installed filters, checkout yields pointers. Set
`GIT_GDRIVE_SKIP_SMUDGE=1` to explicitly keep pointers with filters installed.

The first asset push upgrades the remote manifest to v2; all collaborators need
an updated helper for that repository. Repositories that never use assets stay on
v1. Asset objects and cached content are retained; automatic garbage collection
is not implemented. See [asset format and behavior](docs/assets.md).

## Storage format and limits

Every pushed branch or tag also gets a ZIP snapshot under the selected Drive root:

```text
YOUR_FOLDER_ID/
  branches/
    main.zip
    feature%2Flogin.zip
  tags/
    v1.0.zip
  @.git-remote-gdrive/
    ...packs and manifests...
```

Archive names use the destination branch/tag name, without an object ID. Ref
names are percent-encoded, so `feature/login` becomes `feature%2Flogin` and remains
one filename. An annotated tag's ZIP contains the dereferenced version's files.
ZIPs follow `git archive` behavior, including
`export-ignore`/`export-subst` attributes. They contain tracked files, not `.git`,
uncommitted changes, or untracked files; submodule contents are not fetched. A tag
pointing directly to a blob produces a ZIP with one file named `blob`.

The helper creates `branches/` and `tags/` lazily and records their Drive IDs in the
manifest for reuse across users. Push first uploads the pack and publishes refs;
only after that succeeds does it generate/upload or overwrite ZIPs. Successful
exports are recorded in a separate conditional manifest update containing each
ZIP's ID, object ID, size, and SHA-256 digest. A same-name ZIP is overwritten
in place using its existing Drive file ID; a ZIP is created only if none exists.
Ref deletion removes the current mapping while leaving the last ZIP in place,
and recreating the ref reuses that file. Dry runs and unchanged refs do not upload ZIPs. Older
repositories remain readable; ZIPs are added as their branches/tags are updated.

Existing canonical `branch/` folders are renamed to `branches/` on the next
branch ZIP export, preserving the folder and ZIP file IDs. The manifest retains
the `branch` key for compatibility. Use updated helpers when writing these folders.

If ref publication fails, ZIPs are not touched. If ZIP generation, upload, or its
metadata update fails afterward, Git push stays successful and stderr shows a
warning. Refs are not rolled back. A changed ref's old ZIP mapping is removed when
refs are published, so a missing mapping indicates an unconfirmed/missing export.
Its old ZIP file may remain until a later successful update. An up-to-date push
does not retry failed exports; a later ref update triggers another export attempt.

Drive has no multi-file transaction: ZIPs may temporarily lag behind refs or lack
confirmed metadata. The helper reloads current refs before exporting, skips refs
that changed again, checks the manifest version before overwriting, and sends the
ZIP's ETag when starting its update. A later concurrent push can still race an
in-progress upload. ZIP checksums describe confirmed export bytes; only packs and
manifests retain authoritative Git history.
Existing files with old `<name>-<object-id>.zip` names are left untouched; new
pushes use the fixed names. The helper does not adopt unrelated same-name folders;
multiple matching ZIP files in its archive directory cause an ambiguity error.
Use updated helpers for pushes to preserve ZIP tracking.

The root folder's public `gdrive-repo` property identifies the canonical
`@.git-remote-gdrive/` directory. Its `gdrive-manifest` property points to the current
immutable `manifest.json` by Drive file ID. Drive allows repeated file names, so
clients follow these IDs rather than selecting a folder or manifest by name.

The directory contains immutable pack files and manifest snapshots. A manifest
records the format version, SHA-1 object format, repository/directory IDs, symbolic
HEAD, every ref's object ID, peeled annotated tags, and an ordered list of pack
IDs, sizes, Git pack hashes, and SHA-256 digests. This shared record is how all
users discover the same branches and tags.

Each push uploads at most one non-thin incremental pack containing objects not
reachable from the previous refs. When the active chain already has 16 packs,
the next push writes a complete pack and replaces the active chain. Clone/fetch
downloads packs needed by that snapshot and reuses packs already present in Git's
local object store. Downloads are verified before import with `git index-pack
--strict`; object connectivity is checked before success. Objects are never
uploaded as individual loose files.

The HTTP backend uses resumable sessions with 8 MiB chunks and queries
the acknowledged offset after interrupted requests. Both SDK backends use multipart
uploads below 8 MiB and native SDK resumable uploads for larger files; see the
[backend comparison](docs/drive-backends.md). Requests retry HTTP 429, retryable HTTP 403 rate-limit
responses, selected HTTP 5xx failures, and connection errors with bounded
exponential backoff, jitter, and `Retry-After`. Permission and storage-quota errors
fail immediately. Generated file IDs make retried creation idempotent.

Publishing happens after the pack and manifest uploads finish. The helper rereads
the root pointer and uses its ETag in an `If-Match` metadata patch. A stale
snapshot or HTTP 412 rejects the push, including a force push; fetch and retry.
The patch publishes the directory and manifest IDs together. A missing ETag fails
closed. The v2 backends read the JSON ETag; `sdkv3` reads the file GET response header.
If that header is absent, v3 refuses conditional writes. Live Drive conditional
updates still require opt-in verification.

Current limitations and costs:

- Format v1 supports SHA-1 and full history. SHA-256 and shallow local repositories
  are rejected; shallow/partial transfer options are not advertised.
- Fetch transfers the active snapshot's packs, including objects from refs beyond
  the requested branch. Compaction trades a periodic full upload for a bounded
  active pack count.
- Legacy ZIPs, manifests, superseded packs, and files left by interrupted/conflicting pushes
  are retained. This protects readers using older snapshots but consumes Drive
  quota. Automatic garbage collection is not implemented; do not delete files
  while readers or writers are active.
- Each updated branch/tag uploads a full ZIP snapshot in addition to Git's pack
  data. This adds transfer time and Drive storage proportional to those snapshots.
- Local temporary disk space is needed for ZIP creation and pack creation/download, in addition to
  Git's object store. Transfer memory is bounded by the upload chunk size rather
  than the whole pack. Manifest input is limited to 8 MiB.
- Live Google Drive behavior, including ETag publication and shared-drive access,
  still needs an integration run with real credentials. Automated tests verify
  the protocol against a mock API, not Google's deployed service.

For the detailed format and commit algorithm, see [docs/storage.md](docs/storage.md).

## GitHub Actions

The **Testing** workflow runs `go test -race ./...` and `go vet ./...` on every
push to `main`. Both workflows use the Go version declared in `go.mod`.

Publishing a GitHub Release (including a prerelease) triggers **Build** for its
tag. It cross-compiles all three executables with CGO disabled for these targets:

| Platform | Release archive |
| --- | --- |
| Linux x86_64 | `git-remote-gdrive-linux-x86_64.tar.gz` |
| Linux ARM64 | `git-remote-gdrive-linux-arm64.tar.gz` |
| macOS ARM64 (Apple Silicon) | `git-remote-gdrive-macos-arm64.tar.gz` |
| Windows x86_64 | `git-remote-gdrive-windows-x86_64.zip` |
| Windows ARM64 | `git-remote-gdrive-windows-arm64.zip` |

Each archive contains `git-gdrive`, `git-remote-gdrive`, and `git-remote-gdrive-local` (with `.exe` on Windows),
LICENSE, and README. The workflow attaches the archive and its `.sha256` checksum
to the triggering release using the built-in `GITHUB_TOKEN`; no additional secret
is required. Rerunning a build replaces that platform's matching assets. Releases
must allow asset uploads/replacement; this workflow does not enable immutable
releases. ARM targets mean 64-bit ARM, not 32-bit ARMv7.
