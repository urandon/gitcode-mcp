# Design Package Validation Scenarios: Task 010

## Scope

Task: `010-internal-service-task-3-add-empty-wiki-diagnostic-routing-internal-servic`

This validation exercises offline Go service tests for empty wiki diagnostic routing in `internal/service`, using in-memory cache storage and fake GitCode clients as external-provider mocks.

## Scenarios

### 010-internal-service-task-3-add-empty-wiki-diagnostic-routing-internal-servic-scenario-1

Wiki sync against empty wiki provider (400/404 on GET wiki/contents) returns empty_wiki diagnostic class.

Executable coverage:

- `go test ./internal/service -run '^TestBulkSyncWikiEmptyWikiDiagnosticUnbounded$' -count=1`
- `go test ./internal/service -run '^TestBulkSyncWikiEmptyWikiDiagnosticBounded$' -count=1`

### 010-internal-service-task-3-add-empty-wiki-diagnostic-routing-internal-servic-scenario-2

Diagnostic message includes text referencing CLI init command or GitCode UI step.

Executable coverage:

- `go test ./internal/service -run '^TestNormalizeSyncFailureMapsEmptyWiki$' -count=1`

### 010-internal-service-task-3-add-empty-wiki-diagnostic-routing-internal-servic-scenario-3

Empty_wiki diagnostic is not classified as api_validation or provider_failure.

Executable coverage:

- `go test ./internal/service -run '^TestBulkSyncWikiEmptyWikiDiagnosticUnbounded$' -count=1`
- `go test ./internal/service -run '^TestBulkSyncWikiEmptyWikiDiagnosticBounded$' -count=1`

## Offline Determinism

The validation uses Go tests with in-memory SQLite stores and fake GitCode clients. It does not perform live network, external-provider, credential, or device access.
