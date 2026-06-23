# Decommission Ledger

Schema Version: `triborg.decommission-ledger.v1`

## decommission-1
- Request Task: 1
- Target: Explicit `--live` sync fallback to fixture-shaped provider results
- Category: state_contract
- Action: `replace`
- Verification: Offline CLI live integration test proves `sync --live` reaches the mock HTTP provider and fails if fixture identifiers such as `ISSUE-42` or `WIKI-HOME` are produced.
- Allowlist: none
- Keep Reason: n/a


## decommission-2
- Request Task: 4
- Target: Invalid-token `sync --live` fixture success behavior
- Category: state_contract
- Action: `replace`
- Verification: Offline CLI live integration test with invalid token receives 401/403 live auth failure and confirms mock server request count is greater than zero.
- Allowlist: none
- Keep Reason: n/a


## decommission-3
- Request Task: 5
- Target: Live sync cache population from default fixture records
- Category: test_fixture
- Action: `replace`
- Verification: Offline CLI live sync test verifies cache contains sanitized mock API records and excludes fixture identifiers `ISSUE-42` and `WIKI-HOME`.
- Allowlist: none
- Keep Reason: n/a


## decommission-4
- Request Task: 6
- Target: `create-issue --live` route returning `fixture client is read-only`
- Category: route
- Action: `replace`
- Verification: Offline CLI create issue live test verifies mock server write request, audit/cache confirmation, and absence of `fixture client is read-only` in output and error state.
- Allowlist: none
- Keep Reason: n/a
