# Bounded Sync and Partial State Test Validation Scenarios

Task ID: `013-internal-service-task-4-validate-bounded-sync-and-partial-state-tests-int`
Component: `internal-service`

## Scenario Inventory

### 013-internal-service-task-4-validate-bounded-sync-and-partial-state-tests-int-scenario-1: go test ./internal/service/... passes

**Acceptance Criterion:** `go test ./internal/service/...` passes.

**Verification:** Run `go test ./internal/service/... -count=1`. Zero test failures, zero panics, non-zero exit code only on build or test failure.

### 013-internal-service-task-4-validate-bounded-sync-and-partial-state-tests-int-scenario-2: All bounded sync scenarios verified with mocked paginated providers

**Acceptance Criterion:** All bounded sync scenarios verified with mocked paginated providers.

**Verification:** The following mock-backed test functions exist and pass:
- `TestBulkSyncIssuesBoundedCancelMidway_CancelAfterPage3Progress` — issues cancel midway via progress channel
- `TestBulkSyncIssuesBoundedCancelMidway` — issues cancel midway
- `TestBulkSyncIssuesBoundedTimeout` — issues bounded timeout
- `TestBulkSyncIssuesBoundedProgressEvents` — issues bounded progress events
- `TestBulkSyncIssuesBoundedMaxPages` — issues bounded max pages
- `TestBulkSyncIssuesBoundedMaxRecords` — issues bounded max records
- `TestBulkSyncWikiBoundedPreCancel` — wiki bounded pre-cancel
- `TestBulkSyncWikiBoundedMaxRecords` — wiki bounded max records
- `TestBulkSyncWikiBoundedMaxPages` — wiki bounded max pages
- `TestBulkSyncWikiBoundedCancelMidSync` — wiki bounded cancel mid-sync
- `TestBulkSyncWikiBoundedSingleListWikiPagesCall` — wiki bounded single ListWikiPages call (decommission-3)
- `TestBulkSyncIssuesUnboundedBackwardCompat` — issues unbounded backward compat
- `TestBulkSyncWikiUnboundedBackwardCompat` — wiki unbounded backward compat
- `TestBulkSyncAllBoundedAggregatesProgress` — BulkSyncAll bounded aggregates
- `TestBulkSyncWikiEmptyWikiDiagnosticUnbounded` — wiki empty diagnostic unbounded
- `TestBulkSyncWikiEmptyWikiDiagnosticBounded` — wiki empty diagnostic bounded

All scenarios use `fakeGitCodeClient` as the mocked paginated provider.

### 013-internal-service-task-4-validate-bounded-sync-and-partial-state-tests-int-scenario-3: PartialSyncError fields inspected for success_count, diagnostic class

**Acceptance Criterion:** PartialSyncError fields inspected for success_count, diagnostic class.

**Verification:**
- `TestBulkSyncIssuesBoundedCancelMidway_CancelAfterPage3Progress` asserts `partial.SuccessCount == 30` and `partial.Diagnostic == "sync_cancelled"`
- `TestBulkSyncIssuesBoundedCancelMidway` asserts `partial.SuccessCount == 30` and `partial.Diagnostic == "sync_cancelled"`
- `TestBulkSyncIssuesBoundedTimeout` asserts `partial.Diagnostic == "sync_timeout"`
- `TestBulkSyncWikiBoundedPreCancel` asserts `partial.SuccessCount == 0` and `partial.Diagnostic == "sync_cancelled"`
- `TestBulkSyncWikiBoundedCancelMidSync` asserts `partial.Diagnostic == "sync_cancelled"` and `partial.SuccessCount >= 1`
- `TestBulkSyncWikiEmptyWikiDiagnosticUnbounded` asserts `partial.Diagnostic == "empty_wiki"` and that error is not classified as `api_validation` or `provider_failure`
- `TestBulkSyncWikiEmptyWikiDiagnosticBounded` asserts `partial.Diagnostic == "empty_wiki"` and `result.SuccessCount == 0`, and that error is not classified as `api_validation` or `provider_failure`

### 013-internal-service-task-4-validate-bounded-sync-and-partial-state-tests-int-scenario-4: Progress channel events counted and verified

**Acceptance Criterion:** Progress channel events counted and verified.

**Verification:**
- `TestBulkSyncIssuesBoundedProgressEvents` collects all progress events from the channel, asserts `len(events) >= 3` (at least one per page for 3 pages), and verifies each event has `Collection == "issues"` and `RecordsFetched >= 1`
- `TestProgressEventNonBlockingSend` verifies that `emitProgress` on a full channel does not block (non-blocking send, default case)
- `TestBulkSyncAllBoundedAggregatesProgress` verifies aggregated progress across issues and wiki

### 013-internal-service-task-4-validate-bounded-sync-and-partial-state-tests-int-scenario-5: Wiki tree walker cancellation verified mid-level

**Acceptance Criterion:** Wiki tree walker cancellation verified mid-level.

**Verification:**
- `TestBulkSyncWikiBoundedCancelMidSync` launches a goroutine that cancels the context after 50ms while `BulkSyncWiki` is processing 20 wiki pages. Asserts that a `PartialSyncError` is returned with `Diagnostic == "sync_cancelled"`, `SuccessCount >= 1` (some records committed before cancellation), and total items processed == 20 (all items listed, partial commit). This proves mid-sync cancellation within the wiki bounded path, specifically during the `SyncResources` sub-phase after `ListWikiPages` returns.
- `TestBulkSyncWikiBoundedPreCancel` verifies that cancellation before the call starts returns `SuccessCount == 0` and `Diagnostic == "sync_cancelled"`.
- `TestBulkSyncWikiBoundedSingleListWikiPagesCall` is the decommission-3 verification: proves that bounded wiki sync makes exactly one `ListWikiPages` call (no outer loop wrapper), confirmed by `client.listWikiPagesCallCount == 1`.

## Decommission Verification

### decommission-3: Outer loop wrapper approach for bounded wiki sync — REMOVED

**Verification:** `TestBulkSyncWikiBoundedSingleListWikiPagesCall` confirms that `bulkSyncWikiBounded` makes exactly **one** `ListWikiPages` call with the `WikiBounds` struct passed through. The outer loop paging pattern is replaced by internal bounded traversal inside `ListWikiPages`. The test checks `client.listWikiPagesCallCount == 1` with `MaxRecords: 10`, proving no repeated page-fetching occurs at the service layer.
