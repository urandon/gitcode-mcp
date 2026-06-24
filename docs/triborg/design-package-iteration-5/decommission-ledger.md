# Decommission Ledger

Schema Version: `triborg.decommission-ledger.v1`

## decommission-1
- Request Task: 3
- Target: /api/v5/repos/{owner}/{repo}/issues/{number}/labels add-label route assumption
- Category: route
- Action: `replace`
- Verification: Target CLI add-label tests prove product execution uses the selected issue-update behavior or explicit unsupported diagnostic instead of issuing the old route as a successful mutation path.
- Allowlist: none
- Keep Reason: n/a


## decommission-2
- Request Task: 3
- Target: GitHub-like labels JSON array request contract for GitCode issue create/update
- Category: state_contract
- Action: `replace`
- Verification: Mocked live tests fail when outgoing GitCode issue create/update payloads encode labels as a JSON array rather than the accepted JSON string form.
- Allowlist: none
- Keep Reason: n/a


## decommission-3
- Request Task: 6
- Target: browser web-api.gitcode.com/api/v2/projects/wiki/* as default MCP wiki route
- Category: route
- Action: `keep_internal`
- Verification: Target wiki provider tests and route configuration prove default product wiki sync/read/write paths use /api/v5 repos/{owner}/{repo}.wiki routes or unsupported diagnostics, with browser web-api routes unavailable for default MCP execution.
- Allowlist: none
- Keep Reason: Retain only as documented discovery/fallback research until a stable public-safe non-cookie credential path is validated.


## decommission-4
- Request Task: 7
- Target: 400/schema/decode failures classified as live_transport_failure or configuration failure
- Category: state_contract
- Action: `replace`
- Verification: Regression tests trigger 400, malformed JSON, and schema mismatch responses and prove CLI/MCP diagnostics use API validation or schema/decode classifications instead of transport or configuration classifications.
- Allowlist: none
- Keep Reason: n/a


## decommission-5
- Request Task: 8
- Target: shared fixture/live cache namespace without provenance or isolation state
- Category: state_contract
- Action: `replace`
- Verification: Cache transition tests prove fixture-origin records are distinguishable from live-origin records or cleared/isolated before live reads can present them as live GitCode data.
- Allowlist: none
- Keep Reason: n/a
