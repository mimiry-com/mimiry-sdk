# Changelog

Each release of the Go library and the `mimiry` command. Versions follow semver; before v1.0.0, a
minor release may change the Go functions, and says so here.

## Unreleased

For platform API 1.1.

**Changed:**
- **Locations are location codes:** a country and a number, such as `FI-1`. They are what
  `mimiry availability` lists, and what `--location` takes on `availability`, `session create`
  and `volume create`. Codes from the platform's own vocabulary are refused.
- **`mimiry availability` has a STOCK column.** With `--all`, a row with no stock reads `none`.

**Deprecated:** `Provider` on `ProviderPrice`, `Listing` and `SessionBilling`, and
`Listing.InstanceType`, are no longer sent. `AvailabilityOptions.Provider` and `GPUSpec.Provider`
are refused by the platform.

**Added:** package **`cmdline`**, the command-line framework `mimiry` is built on: commands one or
two words deep, standard-library flags, help, and exit codes (0 done, 1 failed, 2 wrong command
line). Other Go programs can build on it. `mimiry`'s help and errors are unchanged.

**Added:** **tab completion** for bash, zsh and fish: `mimiry completion <shell>` prints the
script, and Homebrew installs it. It asks `mimiry` what to offer, so it never goes stale.

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
