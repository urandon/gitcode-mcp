# Design Package Component: internal-service

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Sync Service

## Summary
Sync Service owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add SyncBounds struct and bounded sync (internal/s
Outcome IDs: outcome-3
Outcome Role: primary_product
Decommission IDs: decommission-3
Change Type: add
Description: Implement the `SyncBounds struct and bounded sync (internal/service/sync_bounds.go)` delta inside `Sync Service`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Sync Service` boundaries where available and add only the missing `SyncBounds struct and bounded sync (internal/service/sync_bounds.go)` behavior.
Detailed Design: Add or change `SyncBounds struct and bounded sync (internal/service/sync_bounds.go)` so it satisfies `Define SyncBounds struct with MaxPages, MaxRecords, ProgressChan fields. Every collection sync path (issues, wiki, comments, PRs) accepts context and SyncBounds. Each page fetch checks ctx.Done() before outbound request. On cancellation or deadline, commit records fetched so far and return PartialSyncError with success_count, total_requested, and typed Diagnostic (sync_cancelled, sync_timeout). Progress events carry collection, page, records_fetched, last_seen_cursor emitted after each page commit.`. Keep ownership inside `Sync Service`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Issues sync: 50 records, page size 10, cancel before page 4 -> PartialSyncError with success_count=30, sync_cancelled diagnostic. Timeout 2s on slow fixture -> PartialSyncError with sync_timeout diagnostic. Progress channel consumer receives >=1 event per page fetched with collection/page/records_fetched fields..
Workload: 1 MM

### Task 2: Change index_repo service delegation (internal/ser
Outcome IDs: outcome-1
Outcome Role: supporting_evidence
Decommission IDs: decommission-2
Change Type: change
Description: Implement the `index_repo service delegation (internal/service/service.go)` delta inside `Sync Service`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Sync Service` boundaries where available and add only the missing `index_repo service delegation (internal/service/service.go)` behavior.
Detailed Design: Add or change `index_repo service delegation (internal/service/service.go)` so it satisfies `Wire index_repo handler to call Service.Index not Service.StaleIndex. Remove StaleIndex call path from MCP lifecycle tool route. The Service.Index method performs full index rebuild and returns index outcome (records indexed, errors).`. Keep ownership inside `Sync Service`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: MCP index_repo tool handler invokes Service.Index. Test observes index outcome (populated index) not stale-index diagnostic. StaleIndex is not called from MCP lifecycle tool path..
Workload: 1 MM

### Task 3: Add Empty wiki diagnostic routing (internal/servic
Outcome IDs: outcome-4
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Implement the `Empty wiki diagnostic routing (internal/service/wiki_sync.go)` delta inside `Sync Service`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Sync Service` boundaries where available and add only the missing `Empty wiki diagnostic routing (internal/service/wiki_sync.go)` behavior.
Detailed Design: Add or change `Empty wiki diagnostic routing (internal/service/wiki_sync.go)` so it satisfies `Route empty wiki detection result (empty_wiki diagnostic from adapter) through service layer. Service surfaces as typed diagnostic with actionable remediation text referencing gitcode-mcp wiki init command or GitCode UI step. Wiki sync returns empty_wiki diagnostic class, not api_validation or provider_failure.`. Keep ownership inside `Sync Service`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Wiki sync against empty wiki provider (400/404 on GET wiki/contents) returns empty_wiki diagnostic class. Diagnostic message includes text referencing CLI init command or GitCode UI step. Not classified as api_validation or provider_failure..
Workload: 1 MM

### Task 4: Validate Bounded sync and partial state tests (int
Outcome IDs: outcome-3, outcome-4
Outcome Role: supporting_evidence
Decommission IDs: decommission-3
Change Type: validate
Description: Implement the `Bounded sync and partial state tests (internal/service/sync_bounds_test.go)` delta inside `Sync Service`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Sync Service` boundaries where available and add only the missing `Bounded sync and partial state tests (internal/service/sync_bounds_test.go)` behavior.
Detailed Design: Add or change `Bounded sync and partial state tests (internal/service/sync_bounds_test.go)` so it satisfies `Write mocked tests: issues sync (50 records, page 10, cancel before page 4 -> PartialSyncError, success_count=30, sync_cancelled). Timeout test (--timeout 2s on slow fixture -> sync_timeout). Progress channel test (>=1 event per page). Wiki sync (recursive tree, cancel mid-traversal -> traversal stops within current level, PartialSyncError with committed records). Wiki empty test (400/404 -> empty_wiki diagnostic class).`. Keep ownership inside `Sync Service`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: go test ./internal/service/... passes. All bounded sync scenarios verified with mocked paginated providers. PartialSyncError fields inspected for success_count, diagnostic class. Progress channel events counted and verified. Wiki tree walker cancellation verified mid-level..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Sync Service` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Sync Service` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
