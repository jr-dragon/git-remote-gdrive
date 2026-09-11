# git-remote-gdrive

Use Google Drive as a Git remote through `gdrive://{folder_id}`. Authentication
is implemented; the remote helper and clone/fetch/push storage are not yet implemented.

## Install

With the Go version specified in `go.mod`:

```sh
go install ./git-gdrive
```

Add your Go binary installation directory to `PATH` to use either `git-gdrive`
or `git gdrive`.

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
reading and managing all Drive files. This supports the planned workflow of
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
- `internal/googleauth`: OAuth flow, callback validation, and credential persistence.
- `internal/browser`: default browser launchers for macOS, Linux, and Windows.

```sh
go test -race ./...
go vet ./...
```

Tests use a local mock token endpoint and temporary credential files. They do not
contact Google or use real credentials. Live authorization requires your own
Desktop app OAuth client and consent in a browser.
