# Scenarios: Validate real CLI live routes

## 013-cli-integration-tests-task-1-validate-real-cli-live-routes-scenario-1

Developer runs `go test ./...`; target product surface is the real `gitcode-mcp` CLI startup path plus cache/audit runtime inspection; expected outcome is that all live-readiness integration scenarios pass offline without real credentials, external network, or OS Keychain access.

Executable validation:

- Run the focused `cmd/gitcode-mcp` real-startup integration subtests that drive operator-style argv through `run(...)` with isolated stdin/stdout/stderr and temporary runtime state.
- Run `go test ./... -count=1` with GitCode credential and live-provider environment scrubbed.
- Run `git diff --check` to verify the validation artifacts and source tree are whitespace-clean.

Expected result:

- All focused CLI live-readiness scenarios pass.
- The full Go test suite passes offline.
- No real GitCode credentials, external network, or OS Keychain are required.

## 013-cli-integration-tests-task-1-validate-real-cli-live-routes-scenario-2

Operator-equivalent triggers include `gitcode-mcp sync --live`, `gitcode-mcp create-issue --live`, `gitcode-mcp auth status`, `gitcode-mcp sync`, and `gitcode-mcp doctor --live --format json`; expected visible/state outcomes include admitted live mock requests, typed missing-credential with zero requests, live auth failure with nonzero requests, mock issue/wiki/comment records and sync events or cache confirmations, create confirmation state, non-secret audit confirmation records, selected-only base URL hits, default fixture sync with zero mock hits, doctor JSON effective fields, and failure on `ISSUE-42`, `WIKI-HOME`, fixture success, or `fixture client is read-only` in live output/state.

Executable validation:

- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-SYNC-VALID` to prove `sync --live` reaches the selected mock HTTP provider, authenticates, populates cache from sanitized mock issue/wiki/comment payloads, and rejects fixture identifiers.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-SYNC-MISSING-CREDENTIAL` to prove typed `missing_credential` failure before any HTTP request.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-SYNC-INVALID-TOKEN-401` to prove invalid credentials reach the live provider, return `live_auth_failure`, and do not fixture-succeed.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-OFFLINE-SYNC-NO-HTTP` to prove default `sync` remains fixture-backed and makes zero mock HTTP requests.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-API-BASE-AUTHORITY` to prove repository binding `api_base_url` is the selected authority and non-selected endpoints receive zero traffic.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-DOCTOR-LIVE-JSON-STARTUP-SNAPSHOT` to prove live doctor JSON reports provider mode, credential source metadata, cache path, selected API base URL, readiness, and no token material.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-CREATE-ISSUE` to prove `auth status` sees the mocked keychain-equivalent credential, `create-issue --live` consumes that same source, sends an authenticated POST with idempotency key, and records cache/audit confirmation without fixture read-only output.
- Run `TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-DOCTOR-LIVE-MOCK-KEYCHAIN` to prove live readiness consistently reports the mocked credential source without OS Keychain access or HTTP contact.

Expected result:

- Mock request counters prove live route admission, zero-request missing credential, nonzero-request auth failure, selected-only base URL routing, and zero-request default sync.
- CLI output and runtime state reject `ISSUE-42`, `WIKI-HOME`, fixture success fallback, token leakage, Authorization leakage, and `fixture client is read-only`.
- Cache runtime assertions find sanitized live mock records and create confirmation state.
- Audit runtime assertions find non-secret live create confirmation metadata.
- Doctor JSON fields match effective startup configuration.

## 013-cli-integration-tests-task-1-validate-real-cli-live-routes-scenario-3

Executable evidence is the stubbed-external-provider Go integration test suite using `httptest.Server` while exercising the target CLI/runtime path.

Executable validation:

- Run focused `cmd/gitcode-mcp` subtests backed by `NewMockGitCodeAPI` / `NewMockGitCodeAPIPair`, which use `httptest.Server` only as the external GitCode-compatible HTTP dependency.
- Assert the target runtime path remains the production CLI startup seam, live provider wiring, repository binding, credential resolution, cache runtime, audit runtime, diagnostics, and doctor readiness implementation.

Expected result:

- Validation fails if the current implementation lacks the required focused subtests or if any focused scenario fails.
- Validation fails if the full Go suite fails.
- Validation remains deterministic and offline by default.
