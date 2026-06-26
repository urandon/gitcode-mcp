# Design Package Validation Scenarios: Task 008

## Scope

Task: `008-internal-service-task-1-add-syncbounds-struct-and-bounded-sync-internal-s`

This validation exercises offline Go service tests for bounded sync behavior in `internal/service`, using in-memory cache storage and fake GitCode clients as external-provider mocks.

## Scenarios

### 008-internal-service-task-1-add-syncbounds-struct-and-bounded-sync-internal-s-scenario-1

Issues sync with 50 records and page size 10 cancels before fetching page 4. The service product path must return `PartialSyncError` with `success_count=30`, `total_requested=50`, and `diagnostic=sync_cancelled` while preserving the partial result.

Executable coverage:

- `go test ./internal/service -run '^TestBulkSyncIssuesBoundedCancelMidway(_CancelAfterPage3Progress)?$' -count=1`

### 008-internal-service-task-1-add-syncbounds-struct-and-bounded-sync-internal-s-scenario-2

Issues sync with an already-expired timeout context representing the slow-fixture timeout path must return `PartialSyncError` with `diagnostic=sync_timeout`.

Executable coverage:

- `go test ./internal/service -run '^TestBulkSyncIssuesBoundedTimeout$' -count=1`

### 008-internal-service-task-1-add-syncbounds-struct-and-bounded-sync-internal-s-scenario-3

A progress channel consumer receives at least one event per fetched page, and every emitted event includes `collection`, `page`, and `records_fetched` fields populated by the service path.

Executable coverage:

- `go test ./internal/service -run '^TestBulkSyncIssuesBoundedProgressEvents$' -count=1`

## Decommission Coverage

`decommission-3` is covered by wiki bounded-sync tests that exercise cancellation within the service/wiki sync path and avoid validating only an external outer-loop wrapper.

Executable coverage:

- `go test ./internal/service -run '^TestBulkSyncWikiBoundedCancelMidPage$' -count=1`

## Offline Determinism

The validation uses Go tests with in-memory SQLite stores and fake GitCode clients. It does not perform live network, external-provider, credential, or device access.
