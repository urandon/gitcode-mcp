# Validation scenarios: 025-internal-mcp-task-1-change-mcp-server-tool-registration-and-parity-val

## 025-internal-mcp-task-1-change-mcp-server-tool-registration-and-parity-val-scenario-1
MCP client connects over stdio to the local MCP server backed by an offline, live-shaped cached issue/wiki dataset. The client invokes all 7 read tools after the cache is populated: `cache_status`, `list_sources`, `get_source`, `sync_status`, `list_chunks`, `search_chunks`, and `search_sources`. Each call must return a non-error JSON-RPC tool result with structured content matching the seeded GitCode cache data.

## 025-internal-mcp-task-1-change-mcp-server-tool-registration-and-parity-val-scenario-2
HTTP/SSE transport starts through the local MCP HTTP/SSE handler with readiness enabled and a client session opened through `/sse`. The same 7 read tools are invoked through the message endpoint. Each call must return a non-error JSON-RPC tool result with structured content matching the same cached issue/wiki dataset used by stdio validation.

## 025-internal-mcp-task-1-change-mcp-server-tool-registration-and-parity-val-scenario-3
Validation test passes against a live-synced-cache-shaped offline cache. Because live validation is disabled for this run, the executable validation uses sanitized issue/wiki cache records and sync/index metadata as the external dependency substitute. The runtime path remains the production MCP server, JSON-RPC tool registry, stdio transport, HTTP/SSE transport, service dispatch, cache reads, and search reads.

## Failure expectations
The validation must fail if any required read tool is not registered, returns a JSON-RPC error, lacks structured content, does not dispatch through cache/search-backed service behavior, omits `issue` or `wiki` from applicable kind schemas, or if HTTP/SSE parity cannot invoke the same read surface as stdio.
