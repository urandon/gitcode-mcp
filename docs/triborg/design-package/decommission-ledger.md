# Decommission Ledger

Schema Version: `triborg.decommission-ledger.v1`

## decommission-1
- Request Task: 1
- Target: MCP tool registry positional indexing in internal/mcp/mcp.go line ~498
- Category: helper
- Action: `replace`
- Verification: go test ./internal/mcp/... passes with new name-based map registry; positional index access removed; adding new lifecycle tool does not shift existing tool handler mappings
- Allowlist: none
- Keep Reason: n/a


## decommission-2
- Request Task: 1
- Target: Service.StaleIndex call path for index_repo MCP tool (incorrectly wiring stale-index diagnostic instead of Service.Index)
- Category: route
- Action: `replace`
- Verification: MCP index_repo tool handler invokes Service.Index; StaleIndex is not called from the MCP lifecycle tool path; test observes index outcome not stale-index diagnostic
- Allowlist: none
- Keep Reason: n/a


## decommission-3
- Request Task: 3
- Target: Outer loop wrapper approach for bounded wiki sync (attempting to limit wiki traversal from outside ListWikiPages)
- Category: helper
- Action: `replace`
- Verification: Wiki sync uses internal bounded/streamed traversal inside ListWikiPages; outer loop pattern removed; cancellation and progress checked within the tree walker; test proves mid-tree cancellation stops traversal
- Allowlist: none
- Keep Reason: n/a


## decommission-4
- Request Task: 5
- Target: Wiki path normalization that produces wiki/Home.md.md for remote Home.md
- Category: state_contract
- Action: `replace`
- Verification: Cached wiki record path stored as wiki/Home.md not wiki/Home.md.md; existing test fixtures updated; go test ./internal/gitcode/... passes
- Allowlist: none
- Keep Reason: n/a


## decommission-5
- Request Task: 7
- Target: Issue create/update serialization that emits labels: [] when no label mutation requested
- Category: state_contract
- Action: `replace`
- Verification: JSON body for issue create/update without labels omits labels key entirely; existing tests updated; test proves labels key absent from serialized body
- Allowlist: none
- Keep Reason: n/a


## decommission-6
- Request Task: 7
- Target: add-comment response decoding that fails on live {id, note_id, body} shape
- Category: helper
- Action: `replace`
- Verification: add-comment decodes live-shaped response with http_attempted=true; malformed body yields schema_decode diagnostic; test proves both success and failure paths
- Allowlist: none
- Keep Reason: n/a


## decommission-7
- Request Task: 9
- Target: internal_error diagnostic class emitted for cache lock contention under concurrent access
- Category: state_contract
- Action: `replace`
- Verification: Lock contention emits cache_busy diagnostic, not internal_error; concurrent readers complete without error; go test ./internal/cache/... passes with new diagnostic type
- Allowlist: none
- Keep Reason: n/a


## decommission-2-2
- Request Task: 2
- Target: legacy surface referenced by request task 2: MCP startup/readiness diagnostics
- Category: unspecified
- Action: `replace`
- Verification: A mocked MCP test starts the server with a cache path pointing to a read-only directory; `tools/list` still returns at least one tool (e.g., `doctor`) and the tool result or server capability block includes `cache_path_unwritable` or equivalent typed diagnostic. A test with a schema-incompatible cache (version > current binary-supported schema version) returns `schema_incompatible` diagnostic. A test with a writer-locked cache returns `cache_lock_contention` diagnostic, not a vanished tool list. A test that injects a cache init failure before the Service is created proves that the minimal MCP server starts, `tools/list` returns `doctor`, and calling `doctor` returns the startup-failure diagnostic with actionable text.
- Allowlist: none
- Keep Reason: n/a
