# 008-cache-runtime-task-1-cache-confirmations-persist Scenarios

## 008-cache-runtime-task-1-cache-confirmations-persist-scenario-1

Developer runs the offline repository acceptance gate and the CLI integration coverage triggers `gitcode-mcp sync --live` against an `httptest.Server` GitCode-compatible mock. The real CLI startup path must construct the live provider, the mock server must receive requests, and the configured cache runtime must persist mock issue/wiki/comment/identity/remote revision/sync event state with remote provenance while `ISSUE-42` and `WIKI-HOME` are absent for the live repo.

Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER$' -count=1 -v` plus `go test ./internal/cache -run 'TestScenario008LiveSyncCacheEvidence$' -count=1 -v`.

## 008-cache-runtime-task-1-cache-confirmations-persist-scenario-2

Operator path triggers `gitcode-mcp create-issue --live` with a valid test token or mocked Keychain-equivalent test credential and an idempotency key. After provider confirmation and refreshed record persistence, the cache runtime must expose `GetCacheConfirmationByKey(repoID, key)` with the mock remote alias and refreshed record id, and `GetRecord(repoID, recordID)` must return the corresponding remote-provenance issue.

Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-LIVE-WRITE-MOCK-KEYCHAIN$' -count=1 -v` plus `go test ./internal/service -run 'TestScenario007WriteLiveCreateAuditCacheConfirmation$' -count=1 -v`.

## 008-cache-runtime-task-1-cache-confirmations-persist-scenario-3

Re-running the same live create operation with the same idempotency key and payload must return the same cache confirmation state without duplicating cache confirmation rows or issuing a second provider create side effect.

Executable evidence: `go test ./internal/service -run 'TestScenario007WriteLiveCreateIdempotentReplay$' -count=1 -v` plus `go test ./internal/cache -run 'TestScenario008CacheConfirmationIdempotentUpsert$' -count=1 -v`.

## 008-cache-runtime-task-1-cache-confirmations-persist-scenario-4

Executable evidence is the stubbed-external-provider CLI integration test using `httptest.Server` plus cache-runtime tests for migration, upsert idempotency, record/comment inspection, and fixture-identifier rejection. Cache confirmation insertion must reject missing target records and required empty fields, schema migration must create `cache_confirmations` and indexes, and the repository-wide `go test ./...` gate must remain offline with real credentials, external network, and OS Keychain disabled.

Executable evidence: `go test ./internal/cache -run 'Test(InitialMigration|Scenario008CacheConfirmationIdempotentUpsert|Scenario008CacheConfirmationRequiresRecord|Scenario008LiveSyncCacheEvidence)$' -count=1 -v` and `go test ./...`.
