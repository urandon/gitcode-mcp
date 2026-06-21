# Decommission Ledger

Schema Version: `triborg.decommission-ledger.v1`

## decommission-1
- Request Task: 1
- Target: sanitizedFixtureClient hard-wired in service.New bypassing any provider selection
- Category: helper
- Action: `replace`
- Verification: grep for sanitizedFixtureClient as the only provider path in service.New returns no matches; provider selection is injected
- Allowlist: none
- Keep Reason: n/a


## decommission-2
- Request Task: 1
- Target: fixture-only client being the sole runtime provider (no live path exists)
- Category: surface
- Action: `replace`
- Verification: A --live flag or config gate activates live provider; go test ./... with no live env vars still passes fixture-only
- Allowlist: none
- Keep Reason: n/a


## decommission-3
- Request Task: 2
- Target: credential layer that reports env/token status but has no native Keychain resolution
- Category: surface
- Action: `replace`
- Verification: auth status reports keychain as a token source when keychain-stored token is present; go test ./... passes without keychain dependency
- Allowlist: none
- Keep Reason: n/a


## decommission-4
- Request Task: 3
- Target: fixture-only sync producing fixture-shaped records (ISSUE-42, WIKI-HOME) as the only sync path
- Category: surface
- Action: `replace`
- Verification: sync --live produces real issue/wiki records from GitCode API; sync without --live still uses fixtures
- Allowlist: none
- Keep Reason: n/a


## decommission-5
- Request Task: 4
- Target: create-issue --live returning 'fixture client is read-only' error
- Category: surface
- Action: `replace`
- Verification: create-issue --live with valid token creates issue on remote; no 'fixture client is read-only' error path remains for live writes
- Allowlist: none
- Keep Reason: n/a


## decommission-6
- Request Task: 6
- Target: stale_index false positive: indexed_at set to zero time and content hash mismatch after fresh sync/index
- Category: state_contract
- Action: `replace`
- Verification: sync_status reports zero stale_index entries after fresh sync/index
- Allowlist: none
- Keep Reason: n/a


## decommission-7
- Request Task: 7
- Target: search_sources returning cache_empty after successful sync/index
- Category: surface
- Action: `replace`
- Verification: search_sources returns non-empty results after sync/index; no cache_empty error path on populated cache
- Allowlist: none
- Keep Reason: n/a


## decommission-8
- Request Task: 8
- Target: MCP kind enum containing source/task/page/decision/handoff instead of issue/wiki
- Category: surface
- Action: `replace`
- Verification: MCP tool schemas show issue and wiki in kind enum; legacy enums removed or deprecated
- Allowlist: none
- Keep Reason: n/a


## decommission-9
- Request Task: 9
- Target: CLI subcommand --help paths returning invalid-query diagnostics
- Category: surface
- Action: `replace`
- Verification: All subcommand --help paths return valid help text and exit 0
- Allowlist: none
- Keep Reason: n/a


## decommission-10
- Request Task: 10
- Target: Silently opening old cache databases without schema version check or migration diagnostic
- Category: state_contract
- Action: `replace`
- Verification: Opening old cache reports schema mismatch with actionable message; migrate-cache upgrades compatible schemas
- Allowlist: none
- Keep Reason: n/a


## decommission-11
- Request Task: 11
- Target: Runtime audit reporting only config and credential status without repo/cache/index/MCP readiness detail
- Category: surface
- Action: `replace`
- Verification: doctor reports all readiness dimensions: version, config, cache, repo, token, live provider, auth probe, sync, index, MCP transport
- Allowlist: none
- Keep Reason: n/a


## decommission-12
- Request Task: 12
- Target: MCP read tools only validated against fixture data, no live-cache parity test
- Category: test_fixture
- Action: `replace`
- Verification: MCP parity validation passes against live-synced cache; all read tools return correct results
- Allowlist: none
- Keep Reason: n/a


## decommission-13
- Request Task: 13
- Target: Documentation that does not describe live sync setup, provider selection, credential flow, or sanitization rules
- Category: doc
- Action: `replace`
- Verification: docs/live-readiness.md, updated architecture.md, cache-and-sync-model.md, and public-safety rules exist and are accurate
- Allowlist: none
- Keep Reason: n/a
