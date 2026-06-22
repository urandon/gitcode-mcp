# Materialized Validation Scenarios

Task: `005-fixture-provider-task-1-guard-fixture-boundary`

## 005-fixture-provider-task-1-guard-fixture-boundary-scenario-1

Operator runs `gitcode-mcp sync` without `--live` against the product CLI with a configured repository and an available mock server; the sync product route completes through fixture-backed offline behavior, cache contains the expected fixture records, and the mock server request count remains zero in a Go CLI integration test.

- Target product surface: real CLI startup route for `gitcode-mcp sync` through default non-live fixture mode, sync service, and cache runtime.
- External dependency policy: an offline `httptest.Server` is available only as a GitCode-compatible external endpoint request counter and must not be contacted.
- Expected result: command succeeds, fixture-backed sync stores deterministic fixture records such as `ISSUE-42` and `WIKI-HOME` in the configured cache, and mock server request count remains zero.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-OFFLINE-SYNC-NO-HTTP' -count=1`, `go test ./internal/service -run 'TestSyncResourcesCachesFixtureRecords' -count=1`, and full `go test ./...`.

## 005-fixture-provider-task-1-guard-fixture-boundary-scenario-2

Operator runs `gitcode-mcp sync --live` through the product CLI against a stubbed external GitCode HTTP server; if fixture IDs `ISSUE-42` or `WIKI-HOME` are produced by this component on the live route, the executable integration test fails with fixture fallback detection.

- Target product surface: real CLI startup route for `gitcode-mcp sync --live` through credential resolution, repository binding, live provider construction, external HTTP adapter, sync service, and cache runtime.
- External dependency policy: offline `httptest.Server` replaces only the external GitCode-compatible HTTP endpoint; the product CLI/runtime path is not mocked.
- Expected result: mock server receives live sync requests, command succeeds, and output/cache evidence must not contain fixture markers `ISSUE-42` or `WIKI-HOME`; fixture marker leakage is classified as fixture fallback and fails the scenario.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER' -count=1` and full `go test ./...`.

## 005-fixture-provider-task-1-guard-fixture-boundary-scenario-3

Operator runs `gitcode-mcp create-issue --live` through the product CLI; if the fixture-provider read-only write error reaches the live product surface as the command result, the executable integration test fails and classifies the condition as fixture fallback rather than successful live behavior.

- Target product surface: real CLI startup route for `gitcode-mcp create-issue --live` through the shared credential source, repository binding, live provider write route, and command result surface.
- External dependency policy: offline `httptest.Server` replaces only the external GitCode-compatible HTTP endpoint; a test keychain-equivalent token source is used without OS Keychain or real credentials.
- Expected result: live create reaches the mock server and command output/error do not contain `fixture client is read-only`; surfacing the typed fixture read-only failure in live mode is classified as fixture fallback and fails the scenario.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-LIVE-WRITE-MOCK-KEYCHAIN' -count=1`, `go test ./internal/gitcode -run 'TestScenario005FixtureReadOnlyTyped' -count=1`, and full `go test ./...`.
