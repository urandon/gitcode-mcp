# MCP Setup

## Overview

gitcode-mcp provides two MCP transport modes:

1. **stdio** — single-client, local process. Recommended for editor integrations that spawn the server as a child process.
2. **HTTP/SSE** — multi-client, shared cache. Recommended when multiple agents or clients need to query the same local cache.

Both modes serve the same MCP tools over the same JSON-RPC 2.0 protocol.

## Stdio mode

### Starting the server

```sh
gitcode-mcp --mcp
```

Or equivalently:

```sh
gitcode-mcp mcp serve --transport stdio
```

For MCP help without starting the server:

```sh
gitcode-mcp mcp --help
```

### Client configuration (generic)

Configure your MCP client to launch:

```json
{
  "command": "gitcode-mcp",
  "args": ["--mcp", "--cache-path", "/path/to/cache.db"]
}
```

Stdio mode uses stdin/stdout for JSON-RPC frames. stderr carries diagnostics.

### Repo-local cache configuration

When the MCP client launches `gitcode-mcp` from inside a Git worktree, the server can use a repo-local cache without a per-client `--cache-path`. Bootstrap the worktree once:

```sh
gitcode-mcp repo init-local \
  --repo example-owner/example-repo \
  --owner example-owner \
  --name example-repo
```

Then launch:

```json
{
  "command": "gitcode-mcp",
  "args": ["--mcp"]
}
```

The command creates `.gitcode/gitcode-mcp.yaml` with `cache_mode: repo-local`, records the repository binding in `<git-worktree>/.gitcode/mcp/cache.db`, and ensures generated state is ignored:

```gitignore
.gitcode/mcp/
```

It does not sync data. Run `gitcode-mcp sync --repo example-owner/example-repo ...` separately when the cache should be populated.

Command-line `--cache-path`, `GITCODE_MCP_CACHE_DIR`, and global `cache_path` still override repo-local discovery.

## HTTP/SSE mode

### Starting the server

```sh
gitcode-mcp mcp serve --transport http-sse --bind 127.0.0.1:9020
```

Use a localhost bind address unless you explicitly intend to expose the server to other clients.

To use another fixed port:

```sh
gitcode-mcp mcp serve --transport http-sse --bind 127.0.0.1:9021
```

### Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/health` | GET | Returns 200 if the server process is alive |
| `/ready` | GET | Returns 200 if the cache is readable and at least one repository is configured |
| `/sse` | GET | SSE endpoint for server-to-client events |
| `/message` | POST | JSON-RPC request endpoint |

### Health check

```sh
curl http://127.0.0.1:9020/health
```

Expected: HTTP 200.

### Readiness check

```sh
curl http://127.0.0.1:9020/ready
```

Returns a JSON object with `ready` boolean and optional `code`/`message`.

Expected readiness codes:

| Code | Meaning |
|---|---|
| (empty) | Ready |
| `cache_unreadable` | Cache database cannot be opened or read |
| `repo_unavailable` | No repositories configured |
| `locked_writer` | Writer lock contention |

### Client configuration (generic HTTP/SSE)

Configure your MCP client with the server URL:

```json
{
  "transport": "sse",
  "url": "http://127.0.0.1:9020"
}
```

### MCP tool access

MCP tool access defaults to `write`, which exposes both read and write tools. This changes discovery only: every mutation still requires `write_mode: "live"` and passes the existing credential, provider, idempotency, audit, and cache-readiness gates.

Select a read-only MCP session explicitly:

```yaml
mcp:
  tools:
    access: read
```

or:

```sh
GITCODE_MCP_TOOL_ACCESS=read gitcode-mcp --mcp
```

In read-only mode the server exposes cache/read/status tools and hides live/cache mutation tools from `tools/list`. A direct `tools/call` for a disabled mutation tool returns `tool_disabled_by_policy` before argument validation, credential resolution, network access, or cache mutation. Set access to `write` explicitly when overriding a read-only parent configuration.

Read-only Codex MCP example:

```json
{
  "command": "gitcode-mcp",
  "args": ["--mcp"],
  "env": {
    "GITCODE_MCP_TOOL_ACCESS": "read"
  }
}
```

Write-enabled Codex MCP example:

```json
{
  "command": "gitcode-mcp",
  "args": ["--mcp"],
  "env": {
    "GITCODE_MCP_TOOL_ACCESS": "write"
  }
}
```

Use separate config files or keyring accounts when different agents need different credentials:

```json
{
  "command": "gitcode-mcp",
  "args": ["--mcp"],
  "env": {
    "GITCODE_MCP_CONFIG": "/path/to/gitcode-mcp-write.yaml",
    "GITCODE_MCP_TOOL_ACCESS": "write",
    "GITCODE_MCP_KEYRING_ACCOUNT": "codex-write"
  }
}
```

The keyring account is non-secret metadata. The token remains in the OS keyring entry selected by `credential.keyring_service` and `credential.keyring_account`.

Zed stdio example for a repo-local cache:

```json
{
  "gitcode-mcp": {
    "command": "gitcode-mcp",
    "args": ["--mcp"],
    "env": {
      "GITCODE_MCP_TOOL_ACCESS": "read"
    }
  }
}
```

When credentials resolve, MCP startup selects the live provider by default for live lifecycle tools. Use `--offline` or `--fixture` only for deterministic fixture sessions. `doctor` reports the active `tool_access` and provider mode so agents can explain why write tools are or are not available.

### MCP tools exposed

Tools are available in both transport modes. Read-only mode lists the cache/read/status subset; write mode lists all current tools:

| Tool | Description |
|---|---|
| `search_sources` | Search cached sources by full-text/token query; not fuzzy or semantic |
| `get_source` | Get a cached source record by stable id |
| `list_sources` | List cached sources with kind/status/limit/offset |
| `list_chunks` | List cached index chunks |
| `search_chunks` | Search cached index chunks by full-text/token query; not fuzzy or semantic |
| `get_snippet` | Get a cached chunk snippet |
| `stale_index_report` | Report missing or stale index state |
| `recent_changes` | List recently updated cached sources |
| `link_check` | Check cached source links for unresolved targets |
| `cache_status` | Report cache storage, WAL, count, and index-warning status |
| `source_backlinks` | List sources that link to the given id |
| `resolve_id` | Resolve a stable id or alias to its local record |
| `sync_status` | Check sync status for a source or the whole cache |
| `export_snapshot` | Export a deterministic snapshot |
| `diff_snapshot` | Diff two snapshots |
| `repo_status` | Report repository binding plus binary identity, cache schema compatibility, issue/comment counts, and issue-comment queue state |
| `maintenance_status` | Report sanitized daemon-managed cache, backfill frontier, content generation, and RAG coverage state |
| `sync_live` | Synchronize selected issue, issue-comment, pull-request, pull-request-comment, or wiki collections into the cache |
| `create_issue` | Create a live issue through the audited write lifecycle |
| `add_issue_comment` | Add a live issue comment through the audited write lifecycle |
| `update_issue_comment` | Update a live issue comment through the audited write lifecycle |
| `update_issue` | Update live issue metadata through the audited write lifecycle |
| `create_pr` | Create a live pull request through the audited write lifecycle |
| `update_pr` | Update live pull request metadata through the audited write lifecycle |
| `list_milestones` | List live repository milestones and refresh cached milestone records |
| `list_push_remote_mirrors` | List live repository push mirrors and refresh credential-redacted cached records |
| `trigger_push_remote_mirror` | Trigger one configured push mirror through the audited write lifecycle |
| `wait_push_remote_mirror` | Poll sanitized mirror status until finished, failed, or timed out |
| `create_milestone` | Create a live milestone through the audited write lifecycle |
| `update_milestone` | Update live milestone metadata through the audited write lifecycle |
| `set_issue_milestone` | Assign a live issue milestone through the audited write lifecycle |
| `clear_issue_milestone` | Clear a live issue milestone through the audited write lifecycle |
| `add_pr_comment` | Add a live pull request comment through the audited write lifecycle |
| `add_pr_review_comment` | Create a live inline pull request review comment through the audited write lifecycle |
| `reply_pr_review_comment` | Reply inside a live pull request review discussion with list readback |
| `link_pr_issue` | Link a pull request to an issue through the GitCode relation API with fallback |
| `create_page` | Create a live wiki page through the audited write lifecycle |
| `update_page` | Update a live wiki page through the audited write lifecycle |
| `delete_page` | Delete a live wiki page through the audited write lifecycle |
| `add_label` | Add a label to a live issue through the audited write lifecycle |
| `index_repo` | Build or refresh the local cache index |
| `auth_status` | Report redacted credential presence and source metadata |
| `doctor` | Report structured server health diagnostics |

MCP write tools require `write_mode: "live"` and use the same service write path as CLI live writes: idempotency keys, provider confirmation, audit records, cache refresh, typed errors, and public-safe diagnostics. `repo_status` identifies the running binary and cache together: it reports effective binary version/commit metadata, detected and expected cache schema versions, cached issue/comment counts, and the durable issue-comment queue summary. This makes an outdated binary, pending comment drain, and missing cached comments distinguishable without consulting the live API. `sync_live` exposes unambiguous `issue_comments` and `pr_comments` selectors. Its legacy `comments` selector follows the selected parent collection: `issues + comments` drains issue comments, `pulls + comments` syncs pull request comments, and selecting both parent kinds with the generic flag is rejected before any sync work. `comments` without a parent retains pull request comment compatibility. `list_milestones` is read-only and does not require `write_mode`; it refreshes cached milestone records from the live list response. `create_issue` requires `title` and accepts `body`, `labels`, optional `milestone`, and `idempotency_key`. `update_issue` accepts the same milestone selector or `clear_milestone: true`; the fields are mutually exclusive. Milestones are resolved and validated in the configured repository before issue mutation, then verified by issue readback. Successful and idempotently replayed receipts return the resolved stable/remote milestone identity or an explicit cleared marker. `create_milestone` requires `title` and `due_on` because GitCode rejects milestone creation without a due date. The dedicated `set_issue_milestone` and `clear_issue_milestone` tools remain available and use the same resolver/readback contract because GitCode can return `milestone: null` in the immediate issue PATCH response even when assignment succeeds. `add_pr_review_comment` requires `number`, `body`, `path`, and a 1-based current-side file `line`; the adapter derives GitCode's provider coordinates and requires path/line readback before success. The deprecated `position` input is not a diff-hunk offset and, if supplied, must equal `line`. Optional `start_line` must be at or before `line`, and `end_line` must equal `line`. `reply_pr_review_comment` requires `number`, `discussion_id`, `parent_comment_id`, and `body`; it validates the parent, avoids a matching duplicate, and requires discussion readback. `link_pr_issue` defaults to `strategy: "auto"`, which first calls the GitCode PR issue relation endpoint. If that endpoint is unsupported, it falls back to a deterministic PR-body marker plus `Fixes #N`. Use `strategy: "description_fallback"` to force the fallback behavior.

`list_push_remote_mirrors` is also read-only and requires only `repo_id`. It
removes destination URL user-info, query strings, and fragments before returning
or caching mirror records. `trigger_push_remote_mirror` requires
`write_mode: "live"` and a caller-provided `idempotency_key`; `mirror_id` may be
omitted only for a repository with exactly one configured mirror.
`wait_push_remote_mirror` accepts an optional RFC3339 `after` barrier and a
bounded `timeout_seconds`. Trigger and wait results contain no destination
field. See [Push Mirror Operations](push-mirrors.md).

Some CLI operations are intentionally not exposed through normal MCP write access. Credential management, raw escape hatches, destructive local cache maintenance, cache resets, and schema migrations must remain CLI-only unless a future capability registry entry documents a stricter MCP confirmation and safety contract.

### Correlation IDs

HTTP/SSE requests carry an `X-Request-ID` header. If not provided by the client, the server generates one. All request logs include the correlation ID.

## First MCP read

After syncing an offline fixture and indexing:

1. Start the MCP server.
2. Open `/sse` and read the announced `/message?session_id=...` endpoint.
3. POST a `tools/call` request for `get_snippet` with `repo_id`, `source_id`, `line_start`, and `line_end`.
4. Verify the SSE response contains the expected fixture snippet.

Example JSON-RPC request (HTTP/SSE):

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "get_snippet",
    "arguments": {
      "repo_id": "example-owner/example-repo",
      "source_id": "ISSUE-42",
      "line_start": 1,
      "line_end": 3
    }
  }
}
```

## Server lifecycle

- The HTTP/SSE server runs until the process receives SIGINT or SIGTERM.
- Sync, index, and write operations are explicit MCP tool calls or CLI commands; routine reads never trigger them automatically.
- Multiple MCP clients can read concurrently from the shared cache.
- Writer operations are serialized and require explicit live intent.
