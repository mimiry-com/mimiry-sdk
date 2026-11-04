# The `mimiry rpc` machine interface — protocol 2

`mimiry rpc` is how programs that are not written in Go use the library. The Python package is built
on it and nothing else. It never reads the human CLI's output, so that output can change freely.

## One call

- One JSON request on **stdin**.
- Zero or more progress lines on **stdout**, then exactly one response line.
- Every line is one JSON object.

```json
{"v": 1, "op": "sessions.list", "params": {"state": ["running"]}, "host": "platform"}
```

| Field | | |
|---|---|---|
| `v` | required | Protocol version. This binary speaks `2`. |
| `op` | required | The operation, see below. `describe` lists them. |
| `params` | | The operation's parameters. **Unknown fields are refused**: a misspelt filter that was silently ignored would match everything. |
| `host`, `key` | | Override `MIMIRY_HOST` / `MIMIRY_SSH_KEY` and the config `mimiry auth` saved, in that order. |
| `timeout_seconds` | | Bounds the whole call. If absent, there is no bound, so set it on waits. |
| `progress` | | `true` writes a progress line for every poll during a wait. |

Unknown fields in the request itself are refused too.

## The response

```json
{"v": 1, "ok": true, "result": { ... }}
{"v": 1, "ok": false, "error": {"kind": "api", "status": 402, "code": "insufficient_balance", "message": "...", "details": { ... }}}
```

**Exit code:**
- `0`: `ok` is true.
- `1`: an error response was written.
- `2`: the input was not a request at all.

A response is written in every case.

### Error kinds

Branch on `kind` first:

| `kind` | Meaning | Also carries |
|---|---|---|
| `usage` | The request is wrong: unknown op, bad params, unsupported `v`. | |
| `config` | No host, no key, a locked key, or a host that is not a host. | `code`: `no_host`, `no_key`, `key_locked`, `key_unavailable` or `invalid_host`. |
| `api` | The API answered with an error. | `status`, plus `code`, the API's own machine code (e.g. `gpu_unavailable`). Then `fields` / `errors` (validation), `retry_after_seconds`, and `details`, the API's whole error body. |
| `network` | No answer from the API at all. | |
| `timeout` | `timeout_seconds` ended the call. | |
| `failed` | A wait ended in failure. | `resource`: the session, volume or organisation operation as last read. |
| `internal` | Anything else, which is a bug. | |

A key that needs a passphrase cannot be unlocked here, because there is nobody to ask. It must be
loaded in ssh-agent.

## Progress lines

These are written during `sessions.wait_ready`, `volumes.wait_provisioned` and
`orgs.wait_operation` when `progress` is true:

```json
{"v": 1, "progress": { ...the session as read... }}
{"v": 1, "progress": null, "transient": "GET ...: HTTP 502 ..."}
```

## Operations

Params use the library's JSON names. Lists answer a page:
`{"items": [...], "total_count", "limit", "offset", "unavailable"}`.

**`unavailable`** counts entries the server could not read. When it is not 0, the list is
incomplete.

**`"all": true`** on a list fetches every page and answers them as one.

**Elevated rights.** Some ops need elevated rights in the organisation they act on. `describe`
lists them in `elevated`, and a tool built on this interface shows them to everyone, marked
"(requires elevated rights)". Without the right, the API refuses the call: kind `api`, status
`403`. There is no way to ask in advance; the call is the question.

**Organisation changes** are carried out asynchronously. Every `orgs.*` op that changes something
answers `{request_id, status, message, status_url, retry_after}` once the change is accepted.
Accepted is not done: some checks are made only when the change is processed, and a refusal then
ends the operation `failed`. `orgs.wait_operation` follows it to the outcome.

| Op | Params | Result |
|---|---|---|
| `describe` | | `{"protocol", "version", "ops", "elevated"}`. Needs no host or key. |
| `auth.token` | | A valid token, cached or new: `{access_token, expires_at, user_id, email, base_url, fingerprint}`. |
| `auth.sign_in` | | A new token, whatever is cached. |
| `auth.sign_out` | | Forgets the cached token. |
| `sessions.create` | The API's create-session body. | `{id, status, message, status_url}` |
| `sessions.list` | `state`, `state_not`, `operation`, `operation_not`, `org_id`, `updated_after`, `updated_before`, `limit`, `offset`, `all` | A page of sessions. |
| `sessions.get` | `id`, `events_tail` | A session. |
| `sessions.terminate` | `id` | `{id, status, message, status_url}` |
| `sessions.logs` | `id`, `tail`, `since` | `{session_id, logs, truncated, lines_requested}` |
| `sessions.wait_ready` | `id`, `interval_seconds`, `not_found_grace_seconds` | The ready or finished session; on failure, kind `failed`. |
| `volumes.create` | The API's create-volume body. | `{id, status, ...}` |
| `volumes.list` | `org_id`, `state`, `state_not`, `operation`, `operation_not`, `attached`, `updated_after`, `updated_before`, `limit`, `offset`, `all` | A page of volumes. |
| `volumes.get` | `id` | A volume. |
| `volumes.delete` | `id` | `{id, status, ...}` |
| `volumes.extend` | `id`, `size_gb` | `{id, status, ...}` |
| `volumes.wait_provisioned` | `id`, `interval_seconds`, `not_found_grace_seconds` | The provisioned volume; on failure, kind `failed`. |
| `gpus.catalog` | | Every GPU model. Needs no key. |
| `gpus.availability` | `gpu_family`, `form_factor`, `min_vram_gb`, `min_gpu_count`, `location`, `provider`, `min_hourly_rate`, `max_hourly_rate`, `include_unavailable`, `full`, `include_cpu` | GPU models with prices and stock. Needs no key. |
| `account.balance` | | The caller's billing account. |
| `account.org_balance` | `id_or_slug` | An organisation's balance. Elevated. |
| `account.transactions` | `created_after`, `created_before`, `type`, `resource_id`, `limit`, `offset`, `all` | A page of transactions. |
| `account.quota` | | The caller's limits. |
| `orgs.list` | `sort`, `dir`, `limit`, `offset`, `all` | A page of the caller's organisations. |
| `orgs.get` | `id_or_slug` | An organisation, with its members and settings. |
| `orgs.create` | `name`, `slug` | Accepted. On completion, `result.organization` is the new organisation. |
| `orgs.update` | `org_id`, `name`, `slug` | Accepted. Elevated. |
| `orgs.delete` | `org_id` | Accepted. Elevated. |
| `orgs.start_wind_down` | `org_id`, `readable_days`, `drain_sessions`, `volume_grace_days`, `reason` | Accepted. Elevated. |
| `orgs.cancel_wind_down` | `org_id` | Accepted. Elevated. |
| `orgs.transfer_ownership` | `org_id`, `new_owner_id` | Accepted. Elevated. |
| `orgs.members` | `org_id`, `limit`, `offset`, `all` | A page of the members, oldest first. |
| `orgs.invite` | `org_id`, `email` or `username`, `role` | Accepted. Elevated. |
| `orgs.set_member_role` | `org_id`, `user_id`, `role` | Accepted. Elevated. |
| `orgs.remove_member` | `org_id`, `user_id` | Accepted. Elevated. |
| `orgs.leave` | `org_id` | Accepted. Removes the caller. |
| `orgs.invitations` | `org_id`, `limit`, `offset`, `all` | A page of the invitations that can still be accepted. |
| `orgs.revoke_invitation` | `org_id`, `invitation_id` | Accepted. Elevated. |
| `orgs.invitation` | `token` | Whether the caller may accept it; the organisation only for the invitee. |
| `orgs.accept_invitation` | `token` | Accepted. |
| `orgs.operation` | `id` | `{request_id, status, result, error, created_at, updated_at}`. Kept for 15 minutes. |
| `orgs.wait_operation` | `id`, `interval_seconds`, `not_found_grace_seconds` | The completed operation; on failure, kind `failed`. |

## Compatibility

- **`v` changes only on an incompatible change.** Adding an operation, a param or a result field
  does not change it.
- **Readers must ignore result fields they do not know.**
- **A caller can check that an operation exists** with `describe` before relying on it.
