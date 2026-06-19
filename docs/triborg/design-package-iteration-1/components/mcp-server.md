# Design Package Component: mcp-server

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: MCP Server

## Summary
The MCP server component adds `internal/mcp` as the cache-first JSON-RPC stdio transport for the approved read tools. The work is additive and limited to the single Component Impact delta for `mcp-server`.

## Top-Level Alignment
`internal/mcp` is a transport adapter peer to `internal/cli`; it owns MCP framing, initialization, tool definitions, tool argument validation, and response/error serialization while delegating product behavior to the shared service-facing interface.

## Tasks

### Task 1: Add MCP stdio server
Outcome IDs: outcome-4
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Add the `internal/mcp` package as the read-first MCP server boundary over stdio JSON-RPC. The package owns line-delimited JSON-RPC framing, MCP initialization, an eight-tool registry, pre-dispatch argument validation, service dispatch, and MCP result/error serialization. Routine tool calls invoke only cache-first service read methods and do not perform GitCode network access.
Existing Behavior / Reuse: Confirmed absent: there is no existing `internal/mcp` package or MCP server implementation. Reuse the planned shared service concepts from the approved architecture by depending on a narrow service-facing interface instead of importing cache, gitcode, index, or CLI internals. Keep the existing CLI scaffold untouched except for later entrypoint integration owned outside this component.
Detailed Design: Add a `Server` entity in `internal/mcp` constructed with an input reader, output writer, diagnostic writer, and a `Service` interface containing `SearchSources`, `GetSource`, `ListSources`, `GetBacklinks`, `ResolveID`, `GetSyncStatus`, `ExportSnapshot`, and `DiffSnapshot`. Add JSON-RPC structures for `Request`, `Response`, `Error`, and `ErrorData`, preserving request `id` values exactly for all responses; requests without `id` are JSON-RPC notifications and are processed without writing a response. `Serve` reads one newline-delimited JSON value at a time, exits cleanly on EOF, rejects invalid JSON with `-32700 Parse error` when an id cannot be decoded, rejects non-object and batch JSON values with `-32600 Invalid request`, routes `initialize`, `tools/list`, and `tools/call`, writes responses as one newline-delimited JSON object to stdout, and writes diagnostics only to stderr.

Define MCP initialization precisely as method `initialize`. Its result is:
`{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":false}},"serverInfo":{"name":"gitcode-mcp","version":"0.1.0"}}`.
If a client sends any unsupported handshake/server-info method such as `server_info`, `mcp/serverInfo`, or `initialized` with an `id`, return `-32601 Method not found`; if `initialized` is sent as a no-id notification, accept it silently and write no response. `tools/list` accepts empty or absent params and returns `{"tools":[...]}` containing exactly these eight definitions in stable order: `search_sources`, `get_source`, `list_sources`, `source_backlinks`, `resolve_id`, `sync_status`, `export_snapshot`, `diff_snapshot`.

Define concrete JSON Schema inputs and validation before service dispatch:
- `search_sources`: object; required `query` string minLength 1; optional `kind` string enum `source|task|page|decision|handoff`, `limit` integer default 20 minimum 1 maximum 100, `offset` integer default 0 minimum 0. Reject unknown field types or invalid ranges with `-32602`.
- `get_source`: object; required `id` string minLength 1; no default fields. Returns full source record.
- `list_sources`: object; optional `kind` enum `source|task|page|decision|handoff`, `status` string minLength 1, `limit` integer default 20 minimum 1 maximum 100, `offset` integer default 0 minimum 0. Empty object lists all cached source kinds subject to defaults.
- `source_backlinks`: object; required `id` string minLength 1; optional `limit` integer default 50 minimum 1 maximum 200, `offset` integer default 0 minimum 0.
- `resolve_id`: object; required `id` string minLength 1; accepts stable ids and aliases exactly as provided.
- `sync_status`: object; optional `id` string minLength 1; when absent returns aggregate cache sync status, when present returns per-record status.
- `export_snapshot`: object; optional `format` string enum `json|markdown` default `json`; optional `inline` boolean default true. The MCP server returns inline content only when the service returns inline content or a path string.
- `diff_snapshot`: object; required `base_id` string minLength 1 and `head_id` string minLength 1; optional `format` string enum `text|json` default `text`.

Define `tools/call` params as object with required `name` string and optional `arguments` object default `{}`. Unknown tool names return `-32601`; malformed `params`, missing `name`, non-object `arguments`, or schema validation failures return `-32602`. Each tool result is serialized as MCP tool content: `content` is an array with one text item summarizing the result for humans, and `structuredContent` contains the exact structured result returned by the service-facing interface. Response shapes are:
- `search_sources`: `structuredContent={"results":[{"id","path","title","kind","status","snippet","score"}],"limit","offset"}` and text is newline-separated `path:snippet` entries.
- `get_source`: `structuredContent={"id","path","title","body","status","kind","labels","created_at","updated_at","remote_alias","links","backlinks"}` and text is the record body or title/body composite.
- `list_sources`: `structuredContent={"results":[{"id","path","title","kind","status","updated_at"}],"limit","offset"}` and text is newline-separated `id path title`.
- `source_backlinks`: `structuredContent={"id","backlinks":[{"id","path","title","kind","link_text"}],"limit","offset"}` and text is newline-separated backlink summaries.
- `resolve_id`: `structuredContent={"id","path","remote_alias","kind","title"}` and text is `id path remote_alias`.
- `sync_status`: `structuredContent={"id","fresh","stale","last_fetched_at","remote_updated_at","reason"}` for per-record calls or `{"fresh_count","stale_count","last_sync_at","cache_empty"}` for aggregate calls; text summarizes freshness.
- `export_snapshot`: `structuredContent={"format","content","path","content_hash"}` and text is inline export content or the path returned by service.
- `diff_snapshot`: `structuredContent={"base_id","head_id","format","diff","changed"}` and text is the diff body.

Define domain-error mapping from service errors into JSON-RPC error code `-32000` with `data={"code":..., "message":...}`. `not_found` is derived from service not-found errors for missing ids/aliases; `stale_cache` from service freshness/staleness errors that still allow cache reads; `cache_empty` from empty-cache search/list/export conditions; `sync_required` from service errors indicating a cache prerequisite is missing and explicit sync/ingest is required. Unexpected non-domain service failures return `-32603 Internal error` with diagnostics on stderr and no secret-bearing data in stdout. The server never substitutes a live GitCode adapter call for missing cache data.
Acceptance Criteria: A developer runs `go test ./internal/mcp/... -run TestIntegration`; the test starts `Server.Serve` against local pipes, sends `initialize`, receives protocol version `2024-11-05`, tool capability fields, and server info, then sends `tools/list` and receives exactly the eight approved tool definitions with the concrete schemas above. The same test sends `tools/call` for `resolve_id` with a known stable id and receives MCP `content` plus `structuredContent` containing `id`, `path`, and `remote_alias`. A developer runs `go test ./internal/mcp/... -run TestSchemasAndResults`; it exercises all eight tool routes with valid arguments, verifies default `limit`, `offset`, and `format` values, verifies each structured response shape, and verifies invalid field types/ranges return `-32602` before service dispatch. A developer runs `go test ./internal/mcp/... -run TestFramingAndErrors`; invalid JSON returns `-32700`, EOF exits without error, no-id notifications write no response, request ids are preserved on success and error, batch requests return `-32600`, unknown tools and unsupported handshake methods return `-32601`, domain errors map to `-32000` data codes `not_found`, `stale_cache`, `cache_empty`, and `sync_required`, and diagnostics appear on stderr rather than stdout.
Workload: 2.4 MM

## Cross-Cutting Constraints
- MCP reads are cache-first — this component must call service read methods only for routine tools so offline coordinator workflows work without GitCode network access
- CLI/MCP behavioral equivalence — tool dispatch must use the same shared service methods as CLI commands so outputs remain semantically aligned
- Stdio framing is the initial transport — HTTP/SSE MCP transport is not part of this component slice
- JSON-RPC error contracts are stable — protocol errors and domain errors must use the approved code mapping for clients to handle degraded cache states

## Data And Control Flow
- MCP client sends one newline-delimited JSON-RPC object — `Server.Serve` owns decode order, EOF handling, notification suppression, and request id preservation
- `initialize` request enters the MCP lifecycle handler — `internal/mcp` returns protocol version `2024-11-05`, tools capability, and `gitcode-mcp` server info before tool use
- `tools/list` request enters the tool registry — `internal/mcp` returns static tool definitions and schemas without touching service or cache
- `tools/call` request enters schema validation — `internal/mcp` validates `name` and `arguments`, applies defaults, and rejects malformed params before service dispatch
- Service result returns to response encoder — `internal/mcp` emits MCP `content` and `structuredContent` on stdout, while diagnostics remain on stderr

## Component Interactions
- `internal/mcp` -> `internal/service` — `Server` depends on a narrow service-facing interface for the eight read-first operations and does not import cache, gitcode adapter, CLI, or index internals directly
- `cmd/gitcode-mcp` -> `internal/mcp` — the entrypoint later starts `Server` in MCP mode with process stdin/stdout/stderr; this component provides the runtime server API
- `MCP client` -> `internal/mcp` — clients use JSON-RPC `initialize`, `tools/list`, and `tools/call` with newline-delimited stdio framing and receive JSON-RPC 2.0 responses

## Rationale
The approved architecture marks `mcp-server` as detailed and assigns it one additive delta: create `internal/mcp` with stdio lifecycle, JSON-RPC framing, tool definitions, and service dispatch. The current repository does not already contain this component, so a single implementation task covers the missing component-local work without adding unrelated package responsibilities.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0234-run_attempt-1/final_message.txt`
