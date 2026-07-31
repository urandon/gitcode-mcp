# Push Mirror Operations

`gitcode-mcp` can list, trigger, and monitor the push mirrors already
configured for a repository. It never needs browser cookies or session tokens,
and it never accepts a destination URL for a trigger.

Creating, updating, and deleting mirror configurations remain out of scope.

## CLI

List configured mirrors and refresh their sanitized cache projection:

```sh
gitcode-mcp list-push-mirrors \
  --repo example-owner/example-repo \
  --format json
```

`push-mirrors` remains a backward-compatible alias for
`list-push-mirrors`.

Trigger one configured mirror:

```sh
gitcode-mcp trigger-push-mirror \
  --repo example-owner/example-repo \
  --mirror-id 17 \
  --live \
  --idempotency-key example-release-17 \
  --format json
```

`--mirror-id` may be omitted only when the repository has exactly one
configured mirror. A live trigger requires both `--live` and a caller-provided
idempotency key.

Wait for a terminal status:

```sh
gitcode-mcp wait-push-mirror \
  --repo example-owner/example-repo \
  --mirror-id 17 \
  --after 2026-07-30T12:00:00Z \
  --timeout-seconds 120 \
  --format json
```

The optional `--after` barrier prevents an old `finished` or `failed` status
from being mistaken for completion of a new trigger. The terminal result is
`finished`, `failed`, or `timeout`.

## MCP

The equivalent tools are:

- `list_push_remote_mirrors`, a read-only live list;
- `trigger_push_remote_mirror`, an audited write;
- `wait_push_remote_mirror`, read-only polling.

Example trigger:

```json
{
  "repo_id": "example-owner/example-repo",
  "mirror_id": "17",
  "write_mode": "live",
  "idempotency_key": "example-release-17"
}
```

Example wait:

```json
{
  "repo_id": "example-owner/example-repo",
  "mirror_id": "17",
  "after": "2026-07-30T12:00:00Z",
  "timeout_seconds": 120
}
```

List and wait remain available with `GITCODE_MCP_TOOL_ACCESS=read`. Trigger is
exposed only with write tool access. All three operations require a configured
live GitCode credential.

## API Compatibility

List and trigger use the GitCode v5 repository routes:

```http
GET /api/v5/repos/{owner}/{repo}/push_remote_mirrors
Authorization: Bearer <configured-token>
Accept: application/json
```

```http
POST /api/v5/repos/{owner}/{repo}/push_remote_mirrors/{mirror-id}
Authorization: Bearer <configured-token>
Content-Type: application/json
Idempotency-Key: <caller-key>

{"force":true}
```

The trigger response is not trusted as a credential-safe record. After a
successful POST, the adapter performs a sanitized list readback and confirms
that the selected mirror still exists.

The GitCode settings UI uses a browser-coupled v2 route. The adapter does not
reproduce browser cookies, JWTs, page metadata, device identifiers, or
double-encoded UI parameters.

## Idempotency and Ambiguous Results

GitCode has not demonstrated a server-side idempotency guarantee for mirror
triggers, so the local audit record is the safety boundary:

- the caller key atomically claims an `in_progress` audit entry before the
  POST, including across processes sharing the cache;
- the POST gets one transport attempt and is never automatically retried;
- a confirmed response replaces the entry with `succeeded`;
- known pre-trigger failures such as forbidden, not found, rate limited, or
  synchronization already running are recorded as `failed` and may be retried;
- a network failure, timeout, malformed response, provider 5xx, or failed
  readback is ambiguous and leaves the entry `in_progress`;
- replaying the same key after an ambiguous result is blocked instead of
  issuing a possible duplicate trigger.

A successful replay returns the audited receipt without another POST.

## Credential Redaction

Mirror destinations may contain credentials for the target hosting provider.
Redaction happens while the adapter decodes a list response, before the service
result or cache graph is constructed:

- URL user-info is removed;
- the entire query string is removed;
- fragments are removed;
- destinations that cannot be parsed as an absolute `http`, `https`, `ssh`, or
  `git` URL become `[redacted]`;
- supported URLs embedded in provider messages receive the same treatment.

The trigger and wait results do not contain a destination field at all. Raw
responses and unredacted destinations must not appear in logs, fixtures, CLI
output, MCP results, cache records, snapshots, or issue/PR evidence.

## Cache Projection

Each successful list or trigger readback refreshes one deterministic cache
record:

- stable id: `PUSHMIRROR-<remote-id>`;
- record type and remote type: `push_remote_mirror`;
- path: `push-mirrors/<remote-id>.md`;
- body: sanitized destination plus non-secret status, failure count, and update
  timestamps.

Existing cache-first list, get, search, and export surfaces can inspect this
projection without network access. Trigger writes and wait polling themselves
always use the live provider.
