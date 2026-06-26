# Design Package Component: internal-cache

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Cache Layer

## Summary
Cache Layer owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Change Cache lock strategy and WAL mode (internal/
Outcome IDs: outcome-9
Outcome Role: primary_product
Decommission IDs: decommission-7
Change Type: change
Description: Implement the `Cache lock strategy and WAL mode (internal/cache/store.go)` delta inside `Cache Layer`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Cache Layer` boundaries where available and add only the missing `Cache lock strategy and WAL mode (internal/cache/store.go)` behavior.
Detailed Design: Add or change `Cache lock strategy and WAL mode (internal/cache/store.go)` so it satisfies `Configure SQLite WAL mode on cache open. Implement shared read lock for read-only command paths (search, list, get) and exclusive write lock for write paths (sync commits, index writes). When exclusive lock is held, other writers receive typed cache_busy diagnostic (retryable), not internal_error. Concurrent readers holding shared locks are not blocked by exclusive write lock.`. Keep ownership inside `Cache Layer`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Two concurrent read commands (search_sources) against same cache path: both complete without internal_error. Writer hold: concurrent readers complete. Two concurrent writers: one returns cache_busy not internal_error. Three goroutines (2 readers + 1 writer): readers complete while writer held; writer gets cache_busy diagnostic. go test ./internal/cache/... passes..
Workload: 1 MM

### Task 2: Add Schema version check against version 11 (inter
Outcome IDs: outcome-2
Outcome Role: supporting_evidence
Decommission IDs: decommission-2-2, decommission-7
Change Type: add
Description: Implement the `Schema version check against version 11 (internal/cache/schema.go)` delta inside `Cache Layer`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Cache Layer` boundaries where available and add only the missing `Schema version check against version 11 (internal/cache/schema.go)` behavior.
Detailed Design: Add or change `Schema version check against version 11 (internal/cache/schema.go)` so it satisfies `On cache open, compare cache schema version against binary-supported version 11. If cache version > 11, produce schema_incompatible diagnostic and enter read-only/minimal mode. If cache version < 11, use existing migration path. Bump currentSchemaVersion constant to 11.`. Keep ownership inside `Cache Layer`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Cache file with schema_version > 11: open returns schema_incompatible diagnostic. Cache file with schema_version 11: opens normally. Cache file with version < 11: existing migration path used. currentSchemaVersion constant is 11..
Workload: 1 MM

### Task 3: Validate Cache concurrency and lock diagnostics te
Outcome IDs: outcome-9
Outcome Role: supporting_evidence
Decommission IDs: decommission-7
Change Type: validate
Description: Implement the `Cache concurrency and lock diagnostics tests (internal/cache/concurrency_test.go)` delta inside `Cache Layer`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Cache Layer` boundaries where available and add only the missing `Cache concurrency and lock diagnostics tests (internal/cache/concurrency_test.go)` behavior.
Detailed Design: Add or change `Cache concurrency and lock diagnostics tests (internal/cache/concurrency_test.go)` so it satisfies `Write runtime tests (no external mocks): two concurrent search_sources complete without internal_error. Writer hold + concurrent readers complete. Two concurrent writers one returns cache_busy. Three goroutines (2 readers + 1 writer) readers complete writer gets cache_busy. Schema version > 11 yields schema_incompatible.`. Keep ownership inside `Cache Layer`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: go test ./internal/cache/... passes. All concurrency scenarios verified with actual WAL-mode SQLite. Lock contention diagnostics are typed cache_busy. No internal_error for lock contention cases..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Cache Layer` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Cache Layer` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
