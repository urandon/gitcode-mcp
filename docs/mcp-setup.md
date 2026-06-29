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

When the MCP client launches `gitcode-mcp` from inside a Git worktree, the server can use a repo-local cache without a per-client `--cache-path`. Add `.gitcode/gitcode-mcp.yaml` to the worktree:

```yaml
cache_mode: repo-local
```

Then launch:

```json
{
  "command": "gitcode-mcp",
  "args": ["--mcp"]
}
```

The resolved cache is `<git-worktree>/.gitcode/mcp/cache.db`. Keep generated state out of commits with:

```gitignore
.gitcode/mcp/
```

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

MCP tool access defaults to `read`. In read-only mode the server exposes cache/read/status tools and hides live/cache mutation tools from `tools/list`. A direct `tools/call` for a disabled mutation tool returns `tool_disabled_by_policy` before argument validation, credential resolution, network access, or cache mutation.

Enable write-capable MCP sessions explicitly:

```yaml
mcp:
  tools:
    access: write
```

or:

```sh
GITCODE_MCP_TOOL_ACCESS=write gitcode-mcp --mcp
```

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
| `search_sources` | Search cached sources by full-text query |
| `get_source` | Get a cached source record by stable id |
| `list_sources` | List cached sources with kind/status/limit/offset |
| `list_chunks` | List cached index chunks |
| `search_chunks` | Search cached index chunks |
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
| `repo_status` | Report configured repository binding and cache readiness state |
| `sync_live` | Synchronize selected live collection records into the cache |
| `add_issue_comment` | Add a live issue comment through the audited write lifecycle |
| `update_issue_comment` | Update a live issue comment through the audited write lifecycle |
| `update_issue` | Update live issue metadata through the audited write lifecycle |
| `create_pr` | Create a live pull request through the audited write lifecycle |
| `update_pr` | Update live pull request metadata through the audited write lifecycle |
| `add_pr_comment` | Add a live pull request comment through the audited write lifecycle |
| `add_pr_review_comment` | Create a live inline pull request review comment through the audited write lifecycle |
| `link_pr_issue` | Link a pull request to an issue through the GitCode relation API with fallback |
| `index_repo` | Build or refresh the local cache index |
| `auth_status` | Report redacted credential presence and source metadata |
| `doctor` | Report structured server health diagnostics |

MCP write tools require `write_mode: "live"` and use the same service write path as CLI live writes: idempotency keys, provider confirmation, audit records, cache refresh, typed errors, and public-safe diagnostics. `add_pr_review_comment` requires `number`, `body`, `path`, and either `line` or `position`; optional `start_line` and `end_line` are forwarded when supplied. `link_pr_issue` defaults to `strategy: "auto"`, which first calls the GitCode PR issue relation endpoint. If that endpoint is unsupported, it falls back to a deterministic PR-body marker plus `Fixes #N`. Use `strategy: "description_fallback"` to force the fallback behavior.

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
