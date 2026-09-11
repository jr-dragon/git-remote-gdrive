# git-remote-gdrive

Use Google Drive as a Git remote through `gdrive://{folder_id}`. The project
provides browser OAuth authentication and a remote helper for clone, fetch, and push.

## Install

With the Go version specified in `go.mod`:

```sh
go install ./git-gdrive ./git-remote-gdrive
```

Add your Go binary installation directory to `PATH`. Alternatively, run `make build`
and add the resulting `build/` directory to `PATH`. Both binaries and Git itself
must be available: `git-gdrive` supplies `git gdrive`, while Git invokes
`git-remote-gdrive` automatically for `gdrive://` URLs.

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
- `internal/drive`: Drive storage, conditional publication, retries, and resumable uploads.
- `internal/googleauth`: OAuth flow, callback validation, and credential persistence.
- `internal/browser`: default browser launchers for macOS, Linux, and Windows.

```sh
go test -race ./...
go vet ./...
```

Tests use local mock OAuth/Drive endpoints and temporary repositories. Integration
tests execute real Git commands against the mock Drive server, including push,
clone, fetch, concurrent initialization, corruption, interrupted upload, and
compaction. They do not contact Google or use real credentials.

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

## Storage format and limits

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

Uploads use resumable sessions with 8 MiB chunks and query the acknowledged offset
after interrupted requests. Requests retry HTTP 429, retryable HTTP 403 rate-limit
responses, selected HTTP 5xx failures, and connection errors with bounded
exponential backoff, jitter, and `Retry-After`. Permission and storage-quota errors
fail immediately. Generated file IDs make retried creation idempotent.

Publishing happens after the pack and manifest uploads finish. The helper rereads
the root pointer and uses its Drive v2 ETag in an `If-Match` metadata patch. A stale
snapshot or HTTP 412 rejects the push, including a force push; fetch and retry.
The patch publishes the directory and manifest IDs together. A missing ETag fails
closed. The client uses Drive v2 because its file metadata exposes ETags.

Current limitations and costs:

- Format v1 supports SHA-1 and full history. SHA-256 and shallow local repositories
  are rejected; shallow/partial transfer options are not advertised.
- Fetch transfers the active snapshot's packs, including objects from refs beyond
  the requested branch. Compaction trades a periodic full upload for a bounded
  active pack count.
- Old manifests, superseded packs, and files left by interrupted/conflicting pushes
  are retained. This protects readers using older snapshots but consumes Drive
  quota. Automatic garbage collection is not implemented; do not delete files
  while readers or writers are active.
- Local temporary disk space is needed for pack creation/download, in addition to
  Git's object store. Transfer memory is bounded by the upload chunk size rather
  than the whole pack. Manifest input is limited to 8 MiB.
- Live Google Drive behavior, including ETag publication and shared-drive access,
  still needs an integration run with real credentials. Automated tests verify
  the protocol against a mock API, not Google's deployed service.

For the detailed format and commit algorithm, see [docs/storage.md](docs/storage.md).
