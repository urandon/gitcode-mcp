# Scenarios: 001-internal-mcp-task-1-change-mcp-tool-registry-internal-mcp-tools.go

## 001-internal-mcp-task-1-change-mcp-tool-registry-internal-mcp-tools.go-scenario-1

MCP client calls `tools/call` with a tool name and the production MCP handler resolves the handler by map key lookup. The executable validation patches an offline copy of `internal/mcp` with a test that starts the MCP server against an in-memory cache, calls `tools/call` for `resolve_id`, and verifies the returned structured content comes from the `resolve_id` handler for the exact requested name.

## 001-internal-mcp-task-1-change-mcp-tool-registry-internal-mcp-tools.go-scenario-2

New lifecycle tool addition proves handler resolution by name. `go test ./internal/mcp/...` passes with map-based registry. The executable validation patches an offline copy of `internal/mcp` with a test-only lifecycle-style registry entry inserted ahead of existing tools, then verifies `tools/call` for an existing tool name still dispatches to that existing tool's handler and `tools/list` remains deterministic and excludes write tools.

## Additional decommission checks

- Positional registry coupling is rejected by asserting `internal/mcp/mcp.go` no longer contains direct `toolDefs[N]` handler registration.
- `tools/list` is exercised through the production MCP JSON-RPC path and must return the deterministic advertised read/cache tool names.
- Blocked write tool calls are exercised through `tools/call` and must return `unsupported_capability` while remaining absent from `tools/list`.
- Unknown tool calls must still return `unknown_tool`.
