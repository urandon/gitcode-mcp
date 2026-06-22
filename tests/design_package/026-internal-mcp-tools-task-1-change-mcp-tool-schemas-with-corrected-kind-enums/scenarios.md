# Validation Scenarios: 026 MCP tool schemas corrected kind enums

## 026-internal-mcp-tools-task-1-change-mcp-tool-schemas-with-corrected-kind-enums-scenario-1

MCP inspector-equivalent offline JSON-RPC query calls the production stdio MCP server via `gitcode-mcp --mcp` and sends `tools/list`. The returned schemas for `list_sources`, `search_sources`, and `search_chunks` must each expose a `kind` property whose enum is exactly:

```json
["issue", "wiki"]
```

This validates that the product MCP schema surface includes the real GitCode source kinds for all required tools.

## 026-internal-mcp-tools-task-1-change-mcp-tool-schemas-with-corrected-kind-enums-scenario-2

The same `tools/list` response is inspected for the decommissioned legacy values. The `kind.enum` arrays for `list_sources`, `search_sources`, and `search_chunks` must not include any of:

```json
["source", "task", "page", "decision", "handoff"]
```

The validation also calls `tools/call` for `list_sources` with a legacy invalid kind (`task`) and requires JSON-RPC invalid params (`-32602`) with an allowed-kind message that mentions `issue` and `wiki` and does not mention legacy values.
