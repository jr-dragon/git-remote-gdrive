# AGENTS.md

## Project Overview

`git-remote-gdrive` is a Go project that enables Git to use Google Drive as a remote repository through the `gdrive://` URL scheme. It integrates Git's remote-helper protocol with the Google Drive API.

The repository is currently a scaffold. The behavior described below is the intended product contract, not a claim that it is already implemented.

## Binaries and User Interface

The project must provide two binaries available on the user's `PATH`:

### `git-gdrive`

- Provides the `git gdrive` command.
- `git gdrive config` opens the user's browser to perform OAuth authentication with Google.
- After successful authentication, saves the user's credentials to `~/.config/git-remote-gdirve/credential`.
- Shares credential loading and refresh logic with `git-remote-gdrive`.

The `git-remote-gdirve` spelling in the credential path is intentional in this specification. Preserve the exact path unless the user explicitly changes the requirement; do not silently normalize it to the project name.

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

- Supports fetching and pushing repository history as implementation progresses. Adding a remote only configures its URL; actual Drive access happens during operations such as clone, fetch, and push.
- Reports missing or unusable credentials with an actionable instruction to run `git gdrive config`.

## Protocol and Storage Design

- Follow the official [gitremote-helpers documentation](https://git-scm.com/docs/gitremote-helpers) for invocation, capability negotiation, command parsing, and responses.
- Implement the mandatory `capabilities` command and advertise only capabilities that are implemented.
- Reserve standard output for protocol responses and payloads. Send logs, progress, and diagnostics to standard error.
- Handle protocol line boundaries, command batches, and end-of-input correctly.
- Validate `gdrive://{folder_id}` URLs before issuing Drive requests.
- Keep command entry points small and separate authentication, credential persistence, remote-helper protocol handling, and Drive storage concerns into reusable Go packages.
- Define and document the Drive storage layout before relying on it. The initial specification does not prescribe an object format, ref layout, locking mechanism, or transfer strategy.
- Preserve Git object integrity and ref consistency. Account for interrupted transfers and concurrent writers when designing updates; do not report success for an incomplete operation.
- Keep operations scoped to the selected repository folder and avoid modifying unrelated Drive files.

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
