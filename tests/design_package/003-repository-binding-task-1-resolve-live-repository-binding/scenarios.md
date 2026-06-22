# Materialized Validation Scenarios

Task: `003-repository-binding-task-1-resolve-live-repository-binding`

## 003-repository-binding-task-1-resolve-live-repository-binding-scenario-1

Operator configures repository binding `api_base_url` to mock server A, also configures a non-authoritative fallback URL to mock server B, and runs `gitcode-mcp sync --live`.

- Target product surface: real CLI startup route for `sync --live` through repository binding into the live provider.
- External dependency policy: offline `httptest.Server` instances replace only external GitCode-compatible HTTP endpoints; server A is the selected repository binding endpoint and server B is a non-authoritative fallback endpoint.
- Expected result: server A receives live sync requests, server B receives zero requests, the command succeeds, and fixture identifiers do not satisfy the scenario.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-API-BASE-AUTHORITY' -count=1` and full `go test ./...`.

## 003-repository-binding-task-1-resolve-live-repository-binding-scenario-2

Operator then runs `gitcode-mcp doctor --live --format json` with the same effective repository binding, cache path, and mocked live API base URL.

- Target product surface: real CLI doctor JSON route for `doctor --live --format json`.
- External dependency policy: no live network; `httptest.Server` provides the configured API base URL but doctor must report the effective startup snapshot without contacting it.
- Expected result: JSON includes provider mode `live-http`, credential source handoff, effective cache path, server A as `api_base_url`, no token material, and zero mock-server requests.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-DOCTOR-LIVE-JSON-STARTUP-SNAPSHOT' -count=1` and full `go test ./...`.

## 003-repository-binding-task-1-resolve-live-repository-binding-scenario-3

Operator configures an invalid or unusable repository live URL and runs `gitcode-mcp sync --live`.

- Target product surface: real CLI startup route for `sync --live` repository-binding resolution before live HTTP provider construction.
- External dependency policy: an offline `httptest.Server` is available only as a request counter and must receive zero requests.
- Expected result: command fails non-zero with a configuration diagnostic naming `api_base_url` before live HTTP construction, and the mock server receives zero requests.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-BINDING-INVALID-URL-NO-HTTP' -count=1` and full `go test ./...`.
