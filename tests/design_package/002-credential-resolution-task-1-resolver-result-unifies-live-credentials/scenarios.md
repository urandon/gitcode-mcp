# Materialized Validation Scenarios

Task: `002-credential-resolution-task-1-resolver-result-unifies-live-credentials`

## 002-credential-resolution-task-1-resolver-result-unifies-live-credentials-scenario-1

Operator runs `gitcode-mcp sync --live` with no `GITCODE_TOKEN` and no injected credential source.

- Target product surface: real CLI startup route for `sync --live`.
- External dependency policy: offline `httptest.Server` mock is available only to count requests and must receive zero requests.
- Expected result: non-zero command result with typed `missing_credential` diagnostic and zero mock-server requests.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-MISSING-CREDENTIAL' -count=1` and full `go test ./...`.

## 002-credential-resolution-task-1-resolver-result-unifies-live-credentials-scenario-2

Operator runs `gitcode-mcp create-issue --live` with `GITCODE_TOKEN` unset and an injected mocked Keychain-equivalent provider.

- Target product surface: real CLI live write route for `create-issue --live`.
- External dependency policy: `httptest.Server` replaces only the GitCode HTTP service; credential source is injected through the configured test provider seam and OS Keychain is not used.
- Expected result: mock server receives an authenticated create request using the shared resolved credential; output does not contain fixture read-only failure or token material.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-LIVE-WRITE-MOCK-KEYCHAIN' -count=1` and full `go test ./...`.

## 002-credential-resolution-task-1-resolver-result-unifies-live-credentials-scenario-3

Operator runs `gitcode-mcp doctor --live --format json` with the same injected mocked Keychain-equivalent credential.

- Target product surface: real CLI doctor JSON route for `doctor --live --format json`.
- External dependency policy: no live network; the mock server URL is configured as the repository API base URL but doctor must not contact it.
- Expected result: JSON reports provider mode `live-http`, credential source metadata `mock-keychain`, cache path, API base URL, and no token material.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-DOCTOR-LIVE-MOCK-KEYCHAIN' -count=1` and full `go test ./...`.
