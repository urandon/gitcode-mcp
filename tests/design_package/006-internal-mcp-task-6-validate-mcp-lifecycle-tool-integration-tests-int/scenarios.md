# Validation Scenarios: MCP lifecycle tool integration tests

## 006-internal-mcp-task-6-validate-mcp-lifecycle-tool-integration-tests-int-scenario-1

`go test ./internal/mcp/...` passes for the current repository state, including the design-package lifecycle validation injected by `run.sh`.

## 006-internal-mcp-task-6-validate-mcp-lifecycle-tool-integration-tests-int-scenario-2

All lifecycle tool integration scenarios are verified through the in-process MCP RPC handler with a mocked Service and no live network: `tools/list` advertises `repo_status`, `sync_live`, `index_repo`, `auth_status`, and `doctor`; write tool names are not advertised; `repo_status` with no binding returns `binding_state: nothing_bound`; `sync_live` with `issues: true` routes to `Service.SyncToCache` and returns a structured fresh result; `index_repo` routes to `Service.Index`, not `Service.StaleIndex`; `search_sources` and `list_sources` return records from the mocked cache/service surface; and `create_issue` returns `unsupported_capability` without credential lookup or provider calls.

## 006-internal-mcp-task-6-validate-mcp-lifecycle-tool-integration-tests-int-scenario-3

Assertions inspect decoded serialized JSON-RPC/MCP response objects, not source text: `tools/list` tool names are decoded from the MCP result, lifecycle tool call structured content is decoded into result structs/maps, unsupported write errors are decoded from JSON-RPC error data, and name-based handler routing is proven by reordering/appending tool definitions while calling lifecycle handlers by name.

## Offline validation command

Run:

```sh
bash tests/design_package/006-internal-mcp-task-6-validate-mcp-lifecycle-tool-integration-tests-int/run.sh
```
