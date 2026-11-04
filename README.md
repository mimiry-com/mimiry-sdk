# mimiry-sdk

The client library for the Mimiry API, and the `mimiry` command built on it.

> **Status: v0.x.** The Go functions here may still change before v1.0.0, and each release says
> so when they do. The Mimiry API it calls has its own version, in its documentation.

## Install

The command, with Go 1.26 or later:

```
go install github.com/mimiry-com/mimiry-sdk/cmd/mimiry@latest
```

Or download a binary for Linux or macOS (amd64 or arm64) from the
[releases](https://github.com/mimiry-com/mimiry-sdk/releases). Each release lists the SHA-256 of
every file in `SHA256SUMS`. Windows is not supported yet.

The library, in your module:

```
go get github.com/mimiry-com/mimiry-sdk/mimiry@latest
```

and `import "github.com/mimiry-com/mimiry-sdk/mimiry"`. Name the package, as here: the module's
root holds no package, so `go get` on the root adds the module without its dependencies.

`mimiry --version` says which one you have.

## Layout

| Path | What it is |
|---|---|
| `mimiry/` | The Go library. It is the one implementation of host resolution, SSH-signature sign-in, token caching, the API calls and their errors. It does no terminal I/O. |
| `internal/sshsig/` | OpenSSH SSHSIG signing, in-process. The output is identical to `ssh-keygen -Y sign`'s. |
| `cmd/mimiry/`, `internal/cli/`, `internal/rpc/` | One binary with two faces over the library: the human CLI, and the machine interface `mimiry rpc`, a versioned JSON contract specified in [`PROTOCOL.md`](PROTOCOL.md). |
| `spec/compute.json`, `spec/org.json` | Copies of the published compute and organisation API documents, as JSON. The tests read them; they are refreshed from the platform when a document changes. |
| `internal/apispec/` | Reads those documents for the tests. |
| `scripts/release.sh`, `CHANGELOG.md` | Builds the release binaries for a tag and publishes them, with that version's notes from the changelog. |
| `python/` *(planned)* | The Python package. It ships one wheel per platform with the binary bundled, and makes every call through the machine interface, so behaviour exists once. |

## Using the library

```go
c, err := mimiry.New(mimiry.Options{
	Host:    "platform",           // a subdomain, hostname or URL
	KeyPath: "~/.ssh/id_ed25519",  // or its .pub, when the key is in ssh-agent
})
```

Or `mimiry.ResolveOptions("", "")`, which takes the host and key from `MIMIRY_HOST` and
`MIMIRY_SSH_KEY`, then from what `mimiry auth` saved, and caches tokens
in `~/.mimiry/tokens` (`$MIMIRY_HOME/tokens`), shared between processes.

Sign-in is an SSH-signature exchange for a short-lived access token. The client caches the token
and renews it when needed. It needs no `ssh-keygen` on the machine.

## Using the command

```
mimiry auth --host platform --key ~/.ssh/id_ed25519      # once; remembers both
mimiry availability
mimiry volume create --name data --size 100 --wait
mimiry session create nvcr.io/nvidia/pytorch:24.01-py3 \
    --name train --gpu A100 --volume data:/mnt/data --wait
```

`mimiry help` lists every command, and `mimiry <command> --help` describes one. Without
`--ssh-key-id` or `--ssh-public-key`, a session lets in the key you signed in with.

```
mimiry org create "Acme" --slug o-acme
mimiry org invite o-acme jane@example.com
mimiry org set-role o-acme jane@example.com admin
```

An organisation change waits until it is done, because some checks are made only then: a change
that was accepted can still be refused. `--no-wait` returns once it is accepted.

`mimiry --version` says which build you have, with the `mimiry rpc` protocol it speaks. It needs
no host or key. The version is the one Go recorded at build time: a tag when installed at one, or
the commit otherwise (`+dirty` when built from a checkout with changes). The library reports the
same value as `mimiry.Version`, and sends it in the User-Agent.

## Elevated rights

Some functions need elevated rights in the organisation they act on, such as changing its
settings or its members. They are available to everyone and marked **(requires elevated
rights)**: in the library's documentation, in `mimiry` help, and in `mimiry rpc describe`. Without
the right, the API refuses the call.

Which functions need them is decided by the published API documents, not here. The tests hold
every marker to the documents (`x-requires-elevated-rights` on an operation): the library's
documentation, the CLI's help and `describe`'s list are each checked against the operations the
function actually calls.

## Supported versions

[`SUPPORTED-VERSIONS`](SUPPORTED-VERSIONS) is the one place the oldest supported Go and Python
versions are decided, with the next step and the date it is due:

- **Go:** the older of the two supported Go releases, today 1.26.
- **Python:** the oldest CPython that is not end-of-life, today 3.11.

`go.mod`'s `go` directive is tested against it, and so is the Python package's `requires-python`
(once the package exists). A test fails once a switch is overdue. These are minimums for users;
releases are built with the current toolchain.

## Tests

```
go test ./...
```

The signature tests use OpenSSH's `ssh-keygen` as the oracle, and fail without it rather than
skip.

The contract test (`mimiry/contract_test.go`) holds the library's types to the documents in `spec/`:
every response and request type must carry exactly the fields its schema declares, every options
type must send exactly the query parameters its operation declares, and every state constant must
be in the document's enum. A difference is allowed only with a written reason.

## Licence

Apache-2.0, see `LICENSE`.
