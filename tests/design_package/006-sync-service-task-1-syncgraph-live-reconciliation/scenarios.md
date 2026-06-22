# Materialized Validation Scenarios

Task: `006-sync-service-task-1-syncgraph-live-reconciliation`

## 006-sync-service-task-1-syncgraph-live-reconciliation-scenario-1

Operator runs `gitcode-mcp sync --live` through the real CLI route with a valid test token against a stubbed external GitCode HTTP server; the sync-service route fetches issues, wiki pages, and comments through the live client, stages `MOCK-COMMENT-1` through the named comment staging/cache graph path, attaches it to the staged `MOCK-ISSUE-100` issue graph by reconciled parent identity or alias, commits it to cache discussion/comment state with `MOCK-WIKI-LIVE`, and `go test ./...` includes an offline CLI integration test proving `ISSUE-42` and `WIKI-HOME` are absent.

- Target product surface: real CLI startup route for `gitcode-mcp sync --live`, live GitCode HTTP client, sync service, comment staging/cache graph path, and cache runtime.
- External dependency policy: an offline `httptest.Server` replaces only the external GitCode-compatible HTTP provider and records request counts.
- Expected result: the mock server is reached, live issue/wiki/comment payloads are committed to cache with `MOCK-COMMENT-1` attached to `MOCK-ISSUE-100`, `MOCK-WIKI-LIVE` is visible in cache state, and fixture markers `ISSUE-42` and `WIKI-HOME` are absent.
- Executable evidence: `run.sh` requires a real CLI live-sync integration subtest for this exact scenario and the sync-service graph/cache scenario `TestScenario006LiveGraphValidStagesIssueWikiComments`.

## 006-sync-service-task-1-syncgraph-live-reconciliation-scenario-2

Operator runs `gitcode-mcp sync --live` with an invalid test token against a stubbed server returning 401 or 403; the product surface reports `live_auth_failure`, no successful fixture sync result is recorded, and the executable CLI integration test proves the mock server request count is greater than zero.

- Target product surface: real CLI startup route for `gitcode-mcp sync --live`, live HTTP provider auth failure handling, sync-service `normalizeSyncFailure`, diagnostics, and cache runtime.
- External dependency policy: an offline `httptest.Server` returns only 401 or 403 for the invalid token path and records request counts.
- Expected result: command fails with `live_auth_failure`, request count is greater than zero, output/cache do not report fixture success, and no fixture-derived successful sync result is recorded.
- Executable evidence: `run.sh` requires a real CLI invalid-token subtest and the sync-service auth-normalization scenario `TestScenario006LiveAuthFailureNormalized`.

## 006-sync-service-task-1-syncgraph-live-reconciliation-scenario-3

Operator runs `gitcode-mcp sync --live` with a live comment missing a provider ID, carrying forbidden fixture IDs, carrying fixture provider/provenance in a live graph, or referencing an unreconciled parent issue; the sync-service rejects the staged graph before cache commit, returns a structured sync failure, and a stubbed-external-provider integration test proves no invalid comment state is visible in the cache runtime.

- Target product surface: live HTTP provider payload intake, sync-service live graph validator, comment parent reconciliation, and cache runtime commit boundary.
- External dependency policy: an offline stubbed provider supplies invalid live comment/graph payloads while the production sync-service/cache runtime path performs validation and commit decisions.
- Expected result: each invalid graph returns structured `live_graph_invalid` failure before cache mutation, and no invalid comment or fixture-derived live state is visible in cache runtime.
- Executable evidence: `run.sh` requires an invalid-live-graph validation test with no cache commit and full `go test ./...`.

## 006-sync-service-task-1-syncgraph-live-reconciliation-scenario-4

Operator runs `gitcode-mcp sync` without `--live` while the mock server is available; the existing fixture-backed sync behavior completes, the mock server request count remains zero, and the local Go test suite proves default sync behavior is unchanged.

- Target product surface: real CLI startup route for default `gitcode-mcp sync`, fixture provider, sync service, and cache runtime.
- External dependency policy: an offline `httptest.Server` is available as a request counter only and must not be contacted.
- Expected result: default sync succeeds through fixture-backed behavior, the mock server request count remains zero, and existing fixture cache behavior is unchanged.
- Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-OFFLINE-SYNC-NO-HTTP$' -count=1`, `go test ./internal/service -run 'TestSyncResourcesCachesFixtureRecords$' -count=1`, and full `go test ./...`.

## 006-sync-service-task-1-syncgraph-live-reconciliation-scenario-5

System runs bulk live sync for issues and wiki together; partial failures remain structured through `SyncResources`, successful live resources are committed atomically per resource, and failed live resources never write fixture-derived cache graphs.

- Target product surface: `BulkSyncIssues`, `BulkSyncWiki`, `BulkSyncAll`, `SyncResources`, live graph validation, partial failure reporting, and cache runtime.
- External dependency policy: offline stubbed live provider responses replace only the external GitCode API while the production bulk sync and cache paths are exercised.
- Expected result: successful live issue/wiki resources commit atomically per resource, failed resources return structured partial failures, and failed live resources never write fixture-derived cache graphs or fixture marker IDs.
- Executable evidence: `run.sh` requires a dedicated bulk live partial-atomicity test plus full `go test ./...`.
