# Validation Scenarios for 010-mcp-server-write-boundary-task-1-block-mcp-write-calls

## Scenario: 010-mcp-server-write-boundary-task-1-block-mcp-write-calls-scenario-1

**Trace:** An MCP client triggers `tools/list` on the production MCP server and sees only read/cache tools, with no issue or wiki mutation tools advertised; executable evidence is an MCP server/API test run by `go test ./...`.

**Verification:** `go test ./internal/mcp/... -run TestIntegration` asserts the `tools/list` response contains exactly 15 read/cache tools and none of the blocked write names (`create-issue`, `update-issue`, `add-label`, `create-page`, `update-page`) are present. The test also asserts that every listed tool is registered in the `toolRegistry`, proving the server is the production MCP server.

**Status:** PASS — `TestIntegration` passes, verifying `tools/list` returns only the 15 read/cache tools. No write tools are advertised.

## Scenario: 010-mcp-server-write-boundary-task-1-block-mcp-write-calls-scenario-2

**Trace:** The same client triggers MCP `tools/call` for `create-issue`, `update-issue`, `add-label`, `create-page`, and `update-page`, and each call returns structured `errorData.Code = "unsupported_capability"` without invoking live network, credentials, or service write paths; executable evidence is a server/API test using the MCP request path.

**Verification:** `go test ./internal/mcp/... -run TestMCPBlockedWriteBoundary` asserts that each of the 5 canonical blocked write names returns a JSON-RPC error with code `-32601` and `errorData.Code = "unsupported_capability"`. The blocked write branch in `mcp.go` returns before the `toolRegistry()` lookup, confirming no service write, live adapter, credential resolution, network client, or cache mutation paths are invoked. The test also asserts read tool parity — `search_sources`, `get_source`, `list_sources` still succeed — proving the blocked-write branch does not interfere with legitimate read/cache dispatch.

**Status:** PASS — `TestMCPBlockedWriteBoundary` passes for all 5 canonical blocked names with correct error structure, and read tool parity is maintained.

## Scenario: 010-mcp-server-write-boundary-task-1-block-mcp-write-calls-scenario-3

**Trace:** A developer runs `go test ./...` and `git diff --check` locally, and both pass without credentials, network, SSH agent, or OS Keychain.

**Verification:** `go test ./...` passes across all packages without credentials, network access, SSH agent, or Keychain. `git diff --check` returns no whitespace warnings.

**Status:** PASS — `go test ./...` passes for all 14 packages. `git diff --check` exits 0 with no output.
