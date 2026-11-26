# Changelog

Each release of the Go library and the `mimiry` command. Versions follow semver; before v1.0.0, a
minor release may change the Go functions, and says so here.

## Unreleased

**Added:**
- **Run commands and copy files in a session** (RC-473), through the session gateway over SSH,
  signed in with your key and checking the session's published host key:
  - Go: `Sessions().Dial` returns a `SessionSSH` with `Exec` (each argument passed literally, the
    exit status returned), `Upload`/`Download` (streams) and `UploadFile`/`DownloadFile`.
  - `mimiry session exec <id> -- <program> [args]`, `session upload`, `session download`. A
    downloaded file is created readable by you only and never replaces an existing file unless
    `--overwrite` is given.
  - `mimiry rpc`: `sessions.exec`, `sessions.upload`, `sessions.download`, and the error kinds
    `session` and `local` (PROTOCOL.md).
  A session started before the gateway (no published host key) is refused.

## v0.6.0

For platform API 1.2. Talks to platforms from `0.4.0-live.6` on; `mimiry session ssh` and
`session proxy` need a platform with the session gateway (API 1.2).

**Changed:**
- **Locations are location codes:** a country and a number, such as `FI-1`. They are what
  `mimiry availability` lists, and what `--location` takes on `availability`, `session create`
  and `volume create`. Codes from the platform's own vocabulary are refused.
- **`mimiry availability` has a STOCK column.** With `--all`, a row with no stock reads `none`.
- **An API error names the fields that failed.** `APIError.Error()` adds one line per field
  (`  image.uri: …`), so `mimiry` no longer stops at "Request validation failed".
- **`WaitReady` (and `--wait`) work for sessions reached through the session gateway.** They
  publish no machine address, only `ssh.host_key`; a running session with either is ready.
  Earlier releases wait for an address that never comes, so update before the platform stops
  publishing it.
- **Official Docker Hub images in short form work:** `mimiry session create ubuntu:22.04`. The
  platform used to refuse a name with no `/`.
- **`mimiry session status` shows `mimiry session ssh <id>`** for a session with a published host
  key, instead of an `ssh` command to the machine's address. A session started before that keeps
  its direct command.

**Deprecated:** `Provider` on `ProviderPrice`, `Listing` and `SessionBilling`, and
`Listing.InstanceType`, are no longer sent. `AvailabilityOptions.Provider` and `GPUSpec.Provider`
are refused by the platform.

**Added:** package **`cmdline`**, the command-line framework `mimiry` is built on: commands one or
two words deep, standard-library flags, help, and exit codes (0 done, 1 failed, 2 wrong command
line). Other Go programs can build on it. `mimiry`'s help and errors are unchanged.

**Added:** **tab completion** for bash, zsh and fish: `mimiry completion <shell>` prints the
script, and Homebrew installs it. It asks `mimiry` what to offer, so it never goes stale.

**Added:** **`mimiry session ssh <id>`** opens a shell on a running session through the Mimiry
gateway, with the session's published host key pinned: ssh checks it on the first connection
instead of trusting whatever key it is shown. Options and a command go after `--`
(`mimiry session ssh <id> -- -L 8888:localhost:8888`, `... -- nvidia-smi`), and ssh's exit status
is passed on. **`mimiry session proxy <id>`** is the same connection as an ssh `ProxyCommand`, for
your own ssh, scp or rsync. `mimiry session status` shows the host key's fingerprint.

**Added:** in Go, `Sessions().Connect` returns a `net.Conn` to a session's SSH server through the
gateway (`GatewayConn`, with `CloseWrite`), and `Sessions().GatewayTicket` the ticket it uses.
`SSHInfo.HostKey` is the key to check. `cmdline.ExitCode` ends a program with a given code.

**Fixed:** **`--` ends the flags.** An argument after it that looks like a flag is now passed as an
argument; it was parsed as a flag.

## v0.5.0

For platform API 1.0. Talks to platforms from `0.4.0-live.1` on.

**Breaking:**
- **Sign-in** uses `POST /api/auth/v1/token`.
- **`APIError.Code` is gone.** Every error is `{error, message}`: `ErrorCode` is a snake_case code
  to branch on, and `Message` is for people.
- **`Orgs().Members`** returns a page and takes `ListOptions`; `AllMembers` iterates every member.
- **`Quota`** has `MaxConcurrentSessions` and `CurrentSessions` (were `MaxConcurrentJobs` and
  `CurrentJobs`).
- **`Volumes().Extend`** sends `PATCH` with `size_gb`.
- **`Org.Settings` and `OrgSettings` are gone.** Neither setting did anything.
- **`mimiry rpc` speaks protocol 2:** `volumes.extend` takes `size_gb`, and `orgs.members` returns a
  page and takes `limit`, `offset` and `all`.

**Changed:** inviting posts to `/invitations`. Money in responses arrives rounded: amounts to the
cent, rates to 6 decimals.

**Fixed:** `--json` on a list command printed Go field names (`Items`, `TotalCount`). It now prints
the list shape `mimiry rpc` uses: `items`, `total_count`, `limit`, `offset`, `unavailable`.

## v0.1.0

The first public release.

- **The `mimiry` command:** sign in with an SSH key, then run GPU sessions, manage volumes,
  organisations, members and invitations, and read availability, balance, quota and
  transactions. `--json` prints the API's JSON. `mimiry --version` says which build you have.
- **The Go library** (`github.com/mimiry-com/mimiry-sdk/mimiry`) does the same from Go: host
  resolution, SSH-signature sign-in, token caching, the API calls and typed errors.
- **`mimiry rpc`**, a versioned JSON interface for other languages (protocol 1, `PROTOCOL.md`).
- Binaries for Linux and macOS, amd64 and arm64. Windows is not supported yet.
