# Scenarios: MockGitCodeAPI harness validate

## 012-mock-gitcode-api-task-1-mockgitcodeapi-harness-validate-scenario-1

Developer runs the offline test suite through `go test ./...`; the target product surface is the real CLI sync/create/doctor startup path exercised by CLI integration tests with `MockGitCodeAPI` as the external GitCode dependency.

Executable validation:

- Run focused CLI startup integration subtests that instantiate `MockGitCodeAPI` and exercise the real `gitcode-mcp` command path via the package entrypoint.
- Run `go test ./... -count=1` to prove the full offline suite remains green without real GitCode credentials, live network, or OS Keychain.

Expected result:

- Focused MockGitCodeAPI CLI scenarios pass.
- Full Go suite passes offline.

## 012-mock-gitcode-api-task-1-mockgitcodeapi-harness-validate-scenario-2

With a valid mocked credential, `gitcode-mcp sync --live` causes issue/wiki/comment counters to increment, returns parseable sanitized JSON, and runtime cache assertions find mock records while rejecting `ISSUE-42` and `WIKI-HOME`.

Executable validation:

- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-SYNC-VALID`.

Expected result:

- `ListIssues`, `ListWikiPages`, and `ListComments` counters are non-zero.
- No unexpected mock requests occur.
- CLI output and cache assertions reject fixture identifiers `ISSUE-42` and `WIKI-HOME`.
- Cache contains sanitized mock records such as `MOCK-ISSUE-100`, `MOCK-WIKI-LIVE`, and `MOCK-COMMENT-1` through runtime assertions.

## 012-mock-gitcode-api-task-1-mockgitcodeapi-harness-validate-scenario-3

With no credential, `gitcode-mcp sync --live` returns typed missing-credential diagnostics and `MockGitCodeAPI.Counts().TotalRequests` remains zero; with an invalid token, the server records at least one request and the CLI reports live auth failure.

Executable validation:

- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-SYNC-MISSING-CREDENTIAL`.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-SYNC-INVALID-TOKEN-401`.

Expected result:

- Missing credential returns a non-zero command exit, emits `missing_credential`, and mock `TotalRequests == 0`.
- Invalid token returns a non-zero command exit, mock `TotalRequests > 0`, mock `AuthFailures > 0`, emits `live_auth_failure`, and does not fall back to fixture success or fixture identifiers.

## 012-mock-gitcode-api-task-1-mockgitcodeapi-harness-validate-scenario-4

With `gitcode-mcp create-issue --live`, the harness captures an authenticated POST plus idempotency metadata and runtime audit/cache confirmation is verified; with plain `gitcode-mcp sync`, all mock counters remain zero; with selected/non-selected base URLs, only the selected server records traffic.

Executable validation:

- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-CREATE-ISSUE`.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-OFFLINE-SYNC-NO-HTTP`.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-API-BASE-AUTHORITY`.

Expected result:

- Live create captures exactly one `create_issue` POST with `AuthorizationOK == true` and idempotency key `cred-write-1`.
- Live create output excludes `fixture client is read-only` and token values.
- Runtime cache/audit confirmation assertions pass for the create idempotency key.
- Plain non-live sync succeeds with `TotalRequests == 0`.
- Selected API server receives live sync traffic while non-selected server remains untouched.
