# Design Package Component: mcp-server-write-boundary

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: MCP Server Write Boundary

## Summary
The MCP Server Write Boundary is affected because iteration 5 keeps MCP read/cache-oriented while CLI remains the supported mutation surface. Existing MCP tool advertisement is already read-only, but exact write tool calls from the target prompt need a component-owned `unsupported_capability` response instead of generic `unknown_tool`.

## Top-Level Alignment
This component implements the approved MCP write exposure decision: read tools are advertised, write tools are absent, and write attempts receive a typed unsupported diagnostic. It supports Task 9 directly and contributes to Task 10 through offline MCP tests under `go test ./...`.

## Tasks

### Task 1: Block MCP Write Calls
Outcome IDs: outcome-9, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: change
Description: The MCP server owns the tool registry, tool advertisement, and `tools/call` dispatch boundary. Keep production MCP tools read/cache-only, and add an explicit unsupported write boundary for exact issue and wiki mutation tool names so clients receive a stable product diagnostic instead of accidental generic JSON-RPC unknown-tool handling. This component-local change provides supporting evidence for the architecture decision that MCP writes are deferred while CLI mutation commands remain the supported write path.
Existing Behavior / Reuse: Reuse the existing MCP `toolDefs`, `toolRegistry`, `toolsList`, and `toolsCall` flow, which already advertises only read/cache tools such as `search_sources`, `get_source`, `list_sources`, `sync_status`, `export_snapshot`, and `diff_snapshot`. Existing source inspection confirms no advertised MCP write tools for issue create/update/label or wiki create/update, so this task preserves advertisement behavior and changes only call-time diagnostics for known write attempts.
Detailed Design: Add a component-local blocked write capability classifier used by `Server.toolsCall` after params parsing and before the normal registry lookup returns `unknown_tool`. The canonical blocked write name set is exactly `create-issue`, `update-issue`, `add-label`, `create-page`, and `update-page`; each canonical name must return a JSON-RPC error with structured `errorData.Code = "unsupported_capability"` instead of `unknown_tool`. Optional compatibility aliases may include `create_issue`, `update_issue`, `add_label`, `create_page`, `update_page`, `create_wiki_page`, `update_wiki_page`, `create-wiki-page`, and `update-wiki-page`; `create_wiki_page` and `update_wiki_page` are aliases only, not replacements for canonical `create-page` and `update-page`. The blocked-write branch must not call service write methods, live adapter methods, credential resolution, network clients, or cache mutation paths. Preserve `tools/list` behavior by keeping blocked write names out of `toolDefs` and `toolRegistry`; the invariant is that a blocked write name can be diagnosed when called but is never advertised as callable. Add MCP server tests that initialize the production server, assert the advertised tool list remains read/cache-only, call all five canonical blocked write names through the MCP `tools/call` path, and assert `errorData.Code = "unsupported_capability"` for each while read tool parity tests prove the read/cache path is unchanged.
Acceptance Criteria: An MCP client triggers `tools/list` on the production MCP server and sees only read/cache tools, with no issue or wiki mutation tools advertised; executable evidence is an MCP server/API test run by `go test ./...`. The same client triggers MCP `tools/call` for `create-issue`, `update-issue`, `add-label`, `create-page`, and `update-page`, and each call returns structured `errorData.Code = "unsupported_capability"` without invoking live network, credentials, or service write paths; executable evidence is a server/API test using the MCP request path. A developer runs `go test ./...` and `git diff --check` locally, and both pass without credentials, network, SSH agent, or OS Keychain.
Workload: 0.4 MM

## Cross-Cutting Constraints
- MCP remains read/cache-oriented while CLI owns mutation — this preserves the approved iteration 5 write-exposure boundary across server, service, and CLI surfaces
- Offline validation must not require credentials or network — MCP write-boundary tests must exercise local server dispatch only and avoid live adapter, Keychain, SSH, or GitCode network paths
- Unsupported write diagnostics must be sanitized — MCP errors must identify capability status without exposing tokens, private repository coordinates, cookies, or raw live API payloads

## Data And Control Flow
- MCP client sends `tools/list` — MCP server — server builds response from `toolDefs` intersected with `toolRegistry`, excluding blocked write capabilities
- MCP client sends `tools/call` for `create-issue`, `update-issue`, `add-label`, `create-page`, or `update-page` — MCP server — `toolsCall` classifies the canonical blocked name before registry lookup and returns `unsupported_capability` without service mutation dispatch
- CLI user runs mutation command — CLI mutation commands — writes remain outside the MCP server boundary and continue through existing CLI/service paths

## Component Interactions
- `mcp_server` -> `MCP client` — Advertises read/cache tools only and returns structured `unsupported_capability` for known write attempts
- `mcp_server` -> `cli_mutation_commands` — Diagnostic message points clients to CLI as the supported mutation surface without invoking CLI code from MCP
- `mcp_server` -> `test_suite` — MCP server tests prove tool advertisement and blocked write diagnostics under offline `go test ./...`

## Rationale
The configured component has one detailed impact delta: keep MCP read-only while making write attempts fail with a typed unsupported diagnostic. Existing MCP advertisement already appears read/cache-only, so the only required component-local change is the call-time write boundary diagnostic and its offline tests.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0315-run_attempt-1/final_message.txt`
