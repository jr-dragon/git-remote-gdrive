# AGENTS.md

## Project Overview

`git-remote-gdrive` is a Go project that enables Git to use Google Drive as a remote repository through the `gdrive://` URL scheme. It integrates Git's remote-helper protocol with the Google Drive API.

`git-gdrive config` implements authentication. `git-remote-gdrive` implements clone, fetch, and push using immutable pack files and versioned manifests. See `docs/storage.md` for the format and publication algorithm.

## Binaries and User Interface

The project must provide three binaries available on the user's `PATH`:

### `git-gdrive`

- Provides the `git gdrive` command.
- `git gdrive config` opens the user's browser to perform OAuth authentication with Google.
- After successful authentication, saves the user's credentials to `~/.config/git-remote-drive/credential`.
- Shares credential loading and refresh logic with `git-remote-gdrive`.
- `git gdrive install` optionally installs repository-local gdrive-assets clean/smudge filters; `--global` supports setup before cloning. `.gitattributes` selects files with `filter=gdrive-assets`. Never silently install filters or rewrite attributes during ordinary clone/fetch/push.

Preserve the specified `git-remote-drive` credential directory name, which differs from the project name.

### `git-remote-gdrive`

- Implements Git's remote-helper protocol for the `gdrive://` scheme.
- Uses the Google Drive API to store and retrieve repository data in the folder identified by `{folder_id}`.
- Uses the credentials saved by `git gdrive config`.
- Enables these workflows:

  ```sh
  git gdrive config
  git clone gdrive://{folder_id}
  git remote add origin gdrive://{folder_id}
  ```

- Supports clone, fetch, pull, push, force push, atomic push batches, dry runs, ref deletion, lightweight tags, and annotated tags. Adding a remote only configures its URL; actual Drive access happens during operations such as clone, fetch, and push.
- Reports missing or unusable credentials with an actionable instruction to run `git gdrive config`.

### `git-remote-gdrive-local`

- Implements the same remote-helper protocol for `gdrive-local:///absolute/path` without OAuth or Drive API access.
- Uses the portable format in `docs/storage-local.md`, with content-addressed objects and a file-based CURRENT pointer. The entire root must remain usable after copying to another path or computer.
- Reuses Git validation, packs, optional assets, progress, and ref-before-ZIP publication. Only user-selected URLs may supply local asset paths.
- Serialize local writes and compare expected versions before publication. Locks cannot protect independently synced copies; document the single-writer handoff requirement.
- Include the helper in Makefile builds and every release platform package.

## Protocol and Storage Design

- Follow the official [gitremote-helpers documentation](https://git-scm.com/docs/gitremote-helpers) for invocation, capability negotiation, command parsing, and responses.
- Implement the mandatory `capabilities` command and advertise only capabilities that are implemented.
- Reserve standard output for protocol responses and payloads. Send logs, progress, and diagnostics to standard error.
- Honor the remote-helper `progress` and `verbosity` options for transport progress. Group all files in one clone/fetch/pull or push task, showing the overall percentage and current file position. Keep byte updates throttled and retry-aware; failed files must not advance task completion. Progress must never change the ref-before-ZIP publication order or suppress operational errors and ZIP warnings.
- Handle protocol line boundaries, command batches, and end-of-input correctly.
- Validate `gdrive://{folder_id}` URLs before issuing Drive requests.
- Keep command entry points small and separate authentication, credential persistence, remote-helper protocol handling, and Drive storage concerns into reusable Go packages.
- Keep Drive operations behind `drive.API`. Preserve the HTTP backend and the official Drive v2/v3 SDK adapters, selected via `GIT_GDRIVE_API_BACKEND=http|sdkv2|sdkv3`. Unset or empty selects `sdkv3` by default. All must share Store validation/CAS behavior and repository compatibility. Compare them with shared mock contracts and benchmarks; never present loopback timing as live Drive performance.
- For the API backend, preserve `docs/storage.md`: PUBLIC root properties identify the canonical storage directory and immutable manifest; never identify repositories by file names alone. The local backend uses its explicit FORMAT marker and CURRENT file and rejects API-format directories.
- Publish new refs only after successful object and manifest uploads, using the expected manifest version and a conditional root metadata update. Never replace this with an unconditional write, even for force pushes.
- Keep the active incremental pack chain bounded. Retain previous snapshots and packs until a separate, safe garbage-collection design is implemented.
- Optional gdrive-assets use canonical SHA-256 pointers and a verified cache in the Git common directory. Upload all missing assets reachable through pushed history before publishing refs, and retain the complete asset index. Asset failures fail the push before ref publication. Repositories with assets require manifest v2 so older helpers reject them; repositories without assets remain v1. Checkout downloads are lazy and must verify size and SHA-256 before emitting bytes. See `docs/assets.md`.
- Only after refs are successfully published, upload branch/tag ZIP snapshots to root-level `branches/` and `tags/` folders. Use fixed percent-encoded ref names ending in `.zip` and overwrite an existing same-name ZIP in place. Use the exact published destination ref object, then save folder/archive IDs in a separate conditional manifest update that preserves current refs. ZIP or ZIP-metadata failures must warn on stderr without failing or rolling back the already-successful Git push. ZIPs are mutable convenience exports; only packs and manifests retain immutable history. Ref deletion leaves the last ZIP in place.
- Preserve Git object integrity and ref consistency. Account for interrupted transfers and concurrent writers when designing updates; do not report success for an incomplete operation.
- Keep operations scoped to the selected repository folder and avoid modifying unrelated Drive files.

## Current Package Boundaries

- `internal/remotehelper` owns protocol framing, capability/option handling, ref validation, push ordering, and progress task setup.
- `internal/repository` owns manifests, Git object validation, pack generation/import, asset reachability, and ZIP generation.
- `internal/drive` owns the shared Drive API interface, the `http`, `sdkv2`, and `sdkv3` adapters, retry/upload behavior, Drive identity checks, and conditional publication.
- `internal/localstore` implements the portable filesystem Store and must remain independent of OAuth and Drive API code.
- `internal/assets` and `internal/gdriveassets` own canonical pointers, verified caching, filter installation, and lazy asset recovery.
- `internal/googleauth` and `internal/browser` own OAuth, credential refresh/persistence, and browser launch behavior.

## Credentials

- Never commit or log credentials, access tokens, refresh tokens, or authorization codes.
- Persist credentials only after authentication succeeds, using restrictive directory and file permissions where supported.
- Avoid leaving partially written credential files or overwriting valid credentials after a failed authentication attempt.
- Keep browser authentication in `git gdrive config`; remote-helper protocol input must remain available for Git commands.

## Development Workflow

- Prefer Language Server Protocol tooling for code analysis when available, such as `gopls` for Go symbol lookup, references, and diagnostics.
- Use the Codex GitHub app for all GitHub interactions. Do not use the `gh` CLI.
- Follow the Go version declared in `go.mod` and existing repository conventions as implementation develops.
- Keep changes focused on the requested task and avoid introducing unsupported behavior or unnecessary dependencies.
- Format changed Go files with `gofmt`. For code changes, run relevant tests and, when applicable, `go test ./...` and `go vet ./...`. Report checks that could not run.
- Test protocol behavior, URL parsing, credential persistence, and storage failure handling using isolated temporary directories and mocked Drive API responses.
- Keep tests independent of the user's real credentials and Drive data. Make live integration tests explicit and opt-in.
- Update user-facing documentation when commands, authentication behavior, or repository storage expectations change.
