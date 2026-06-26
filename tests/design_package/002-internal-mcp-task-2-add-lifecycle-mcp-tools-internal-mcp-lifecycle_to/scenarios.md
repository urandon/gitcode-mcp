# Materialized Validation Scenarios

## 002-internal-mcp-task-2-add-lifecycle-mcp-tools-internal-mcp-lifecycle_to-scenario-1

MCP client calls `tools/list` against a writable cache through the production MCP stdio path. The response must include lifecycle tool names `repo_status`, `sync_live`, `index_repo`, `auth_status`, and `doctor` in the advertised tool set.

## 002-internal-mcp-task-2-add-lifecycle-mcp-tools-internal-mcp-lifecycle_to-scenario-2

MCP client calls `tools/call` for `repo_status` with no repository binding arguments against an otherwise writable cache. The structured tool result must report `binding_state: nothing_bound` rather than returning an opaque error.

## 002-internal-mcp-task-2-add-lifecycle-mcp-tools-internal-mcp-lifecycle_to-scenario-3

MCP client calls `tools/call` for `sync_live` with the issues selector enabled against an offline fixture-backed service path. The structured result must contain at least one sync result and a positive `fresh_count`.

## 002-internal-mcp-task-2-add-lifecycle-mcp-tools-internal-mcp-lifecycle_to-scenario-4

MCP client calls `tools/call` for `index_repo`. The structured result must be an index operation result from `Service.Index`, with command/status indicating the index path, not a stale-index diagnostic from `Service.StaleIndex`.

## 002-internal-mcp-task-2-add-lifecycle-mcp-tools-internal-mcp-lifecycle_to-scenario-5

After lifecycle sync, MCP client calls `list_sources` through `tools/call` and receives synced cached issue records. The validation also runs read/search MCP parity over the same production MCP call path to prove read tools remain usable with synced records.
