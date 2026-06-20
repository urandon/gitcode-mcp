# Decommission Ledger

Schema Version: `triborg.decommission-ledger.v1`

## decommission-1
- Request Task: 6
- Target: Coordinator shell-only read workflow for snippets, recent changes, link checks, stale-index reports, cache status, chunks, backlinks, and sync status
- Category: surface
- Action: `replace`
- Verification: MCP JSON-RPC/server tests show equivalent read results through MCP tools without requiring shell-only coordinator commands for the covered queries
- Allowlist: none
- Keep Reason: n/a


## decommission-2
- Request Task: 9
- Target: Fake-success or stub-like remote write contract for issue/wiki operations
- Category: state_contract
- Action: `replace`
- Verification: CLI/API tests prove unavailable write adapters return actionable errors and cannot persist successful audit events
- Allowlist: none
- Keep Reason: n/a


## decommission-3
- Request Task: 11
- Target: diff_snapshot behavior that compares current/current for arbitrary unresolved snapshot ids
- Category: state_contract
- Action: `replace`
- Verification: Snapshot tests prove unknown base_id or head_id returns not-found instead of changed:false
- Allowlist: none
- Keep Reason: n/a


## decommission-4
- Request Task: 4
- Target: Fixture-only active cache bootstrap that leaves real coordinator queries returning cache_empty
- Category: state_contract
- Action: `replace`
- Verification: Fixture-backed and optional live sync tests prove configured issue/wiki records populate cache and offline reads return nonempty results
- Allowlist: none
- Keep Reason: n/a


## decommission-5
- Request Task: 7
- Target: Stdio-only MCP runtime as the sole product transport
- Category: surface
- Action: `replace`
- Verification: Runtime transport tests prove both stdio and HTTP/SSE server mode are available, with shared-cache multi-client reads over HTTP/SSE
- Allowlist: none
- Keep Reason: n/a


## decommission-6
- Request Task: 2
- Target: Unscoped remote aliases such as issue:42 and wiki:Home as globally unique identities
- Category: state_contract
- Action: `replace`
- Verification: Two-repository fixture tests prove alias collisions are rejected or disambiguated by repo_id
- Allowlist: none
- Keep Reason: n/a


## decommission-7
- Request Task: 3
- Target: Opaque config discovery that requires reading source code to know active paths and overrides
- Category: surface
- Action: `replace`
- Verification: CLI tests prove config locate and config show --redacted expose active config and precedence without secret disclosure
- Allowlist: none
- Keep Reason: n/a


## decommission-8
- Request Task: 11
- Target: export_snapshot output that silently omits chunks or citable ranges when indexing has not run
- Category: state_contract
- Action: `replace`
- Verification: Export tests prove missing index state produces explicit warnings and indexed state includes chunks and citation ranges
- Allowlist: none
- Keep Reason: n/a
