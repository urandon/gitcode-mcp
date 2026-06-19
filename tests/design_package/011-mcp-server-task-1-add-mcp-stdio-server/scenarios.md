# Validation Scenarios

## 011-mcp-server-task-1-add-mcp-stdio-server-scenario-1
A developer runs `go test ./internal/mcp/... -run TestIntegration`; the test starts `Server.Serve` against local pipes, sends `initialize`, receives protocol version `2024-11-05`, tool capability fields, and server info, then sends `tools/list` and receives exactly the eight approved tool definitions with the concrete schemas above.

## 011-mcp-server-task-1-add-mcp-stdio-server-scenario-2
The same test sends `tools/call` for `resolve_id` with a known stable id and receives MCP `content` plus `structuredContent` containing `id`, `path`, and `remote_alias`.

## 011-mcp-server-task-1-add-mcp-stdio-server-scenario-3
A developer runs `go test ./internal/mcp/... -run TestSchemasAndResults`; it exercises all eight tool routes with valid arguments, verifies default `limit`, `offset`, and `format` values, verifies each structured response shape, and verifies invalid field types/ranges return `-32602` before service dispatch.

## 011-mcp-server-task-1-add-mcp-stdio-server-scenario-4
A developer runs `go test ./internal/mcp/... -run TestFramingAndErrors`; invalid JSON returns `-32700`, EOF exits without error, no-id notifications write no response, request ids are preserved on success and error, batch requests return `-32600`, unknown tools and unsupported handshake methods return `-32601`, domain errors map to `-32000` data codes `not_found`, `stale_cache`, `cache_empty`, and `sync_required`, and diagnostics appear on stderr rather than stdout.
