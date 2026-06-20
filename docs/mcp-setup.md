# MCP Setup

## Overview

gitcode-mcp provides two MCP transport modes:

1. **stdio** — single-client, local process. Recommended for editor integrations that spawn the server as a child process.
2. **HTTP/SSE** — multi-client, shared cache. Recommended when multiple agents or clients need to query the same local cache.

Both modes serve the same 15 MCP tools over the same JSON-RPC 2.0 protocol.

## Stdio mode

### Starting the server

```sh
gitcode-mcp --mcp
```

Or equivalently:

```sh
gitcode-mcp mcp serve --transport stdio
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

### MCP tools exposed

All 15 tools are available in both transport modes:

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

### Correlation IDs

HTTP/SSE requests carry an `X-Request-ID` header. If not provided by the client, the server generates one. All request logs include the correlation ID.

## First MCP read

After syncing fixtures and indexing:

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
- Sync and index operations are explicit CLI commands, never triggered by MCP client requests.
- Multiple MCP clients can read concurrently from the shared cache.
- Writer operations (sync, index) are serialized and require explicit CLI invocation.
