# 004-live-provider-task-1-httpclient-enforce-live-contract Validation Scenarios

## 004-live-provider-task-1-httpclient-enforce-live-contract-scenario-1

Operator runs `gitcode-mcp sync --live` with a valid test token and repository-selected mock base URL through the real CLI startup product route. The offline CLI integration test `cmd/gitcode-mcp::TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER` must prove the live provider sends authenticated issue/wiki/comment reads to `httptest.Server`, mock request count is greater than zero, fixture identifiers `ISSUE-42` and `WIKI-HOME` are absent from CLI output, and the scenario remains executable under `go test ./...`.

Additional provider-level runtime evidence is supplied by `internal/gitcode::TestScenario004ReadRouteContract`, which validates Authorization and Accept headers, issue/wiki/comment endpoint paths, decoded mock records, and no fixture identifiers in live route paths.

## 004-live-provider-task-1-httpclient-enforce-live-contract-scenario-2

Operator runs `gitcode-mcp sync --live` with an invalid token against a mock 401/403 endpoint. The offline provider/runtime tests `internal/gitcode::TestScenario004AuthAfterRequest` and `internal/provider/live::TestAdapterScenario004AuthAfterRequest` must prove at least one live HTTP request is sent before auth classification, 401 maps to `ErrAuthExpired`, 403 maps to `ErrForbidden`, and no fixture success path is used.

## 004-live-provider-task-1-httpclient-enforce-live-contract-scenario-3

Operator runs `gitcode-mcp create-issue --live` with `GITCODE_TOKEN` unset but a mocked Keychain-equivalent credential source resolved by startup. The offline CLI integration test `cmd/gitcode-mcp::TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-LIVE-WRITE-MOCK-KEYCHAIN` must prove the live provider receives the resolved token, sends an authenticated POST to the mock server with deterministic idempotency metadata, returns success without `fixture client is read-only`, and does not leak token material.

Additional provider-level runtime evidence is supplied by `internal/gitcode::TestScenario004CreateIssueContract`, which validates POST method, Authorization header, idempotency key, complete write confirmation metadata, remote id/number, response hash/fingerprint, and absence of fixture read-only behavior.

## 004-live-provider-task-1-httpclient-enforce-live-contract-scenario-4

Operator configures selected and non-selected API endpoints and runs `gitcode-mcp sync --live`. The offline CLI integration test `cmd/gitcode-mcp::TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-API-BASE-AUTHORITY` must prove repository binding `api_base_url` is the selected authority, selected mock server hit count is greater than zero, alternate endpoint hit count is zero, and the scenario remains executable under `go test ./...`.

Additional provider-level runtime evidence is supplied by `internal/gitcode::TestScenario004SelectedBaseURLOnly`, which validates the HTTP client uses only the configured selected base URL and never the fallback endpoint.
