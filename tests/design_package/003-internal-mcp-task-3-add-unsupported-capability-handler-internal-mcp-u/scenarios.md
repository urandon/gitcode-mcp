# Materialized Validation Scenarios

## 003-internal-mcp-task-3-add-unsupported-capability-handler-internal-mcp-u-scenario-1

MCP client calls `tools/call` with the canonical write tool name `create_issue` through the production MCP stdio path. The JSON-RPC response must be a structured `unsupported_capability` diagnostic rather than invoking a mutation handler or returning an untyped internal failure.

## 003-internal-mcp-task-3-add-unsupported-capability-handler-internal-mcp-u-scenario-2

MCP client calls unsupported write tool names through `tools/call`. The unsupported capability path must complete before any credential or auth lookup path is reached; the validation exercises this by running with credentials unset and requiring the response to remain `unsupported_capability`, not `credential_unavailable` or an auth-related diagnostic.

## 003-internal-mcp-task-3-add-unsupported-capability-handler-internal-mcp-u-scenario-3

MCP client calls unsupported write tool names while an external provider mock is available. The provider mock must record zero outbound HTTP calls, proving the write tool was blocked at the MCP capability boundary before any live provider path.

## 003-internal-mcp-task-3-add-unsupported-capability-handler-internal-mcp-u-scenario-4

MCP client calls `tools/list` through the production MCP stdio path. The serialized tool list must omit write tool names `create_issue`, `update_issue`, `add_comment`, `create_page`, and `update_page` while retaining the registered read/lifecycle tools.
