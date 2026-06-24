# Design Package Component: cache-provenance-layer

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Cache Provenance Layer

## Summary
Cache Provenance Layer owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add cache provenance schema, read filters, sync wr
Outcome IDs: outcome-8, outcome-10
Outcome Role: primary_product
Decommission IDs: decommission-5
Change Type: add
Description: Implement the `cache provenance schema, read filters, sync writes, and `cache reset --live` command` delta inside `Cache Provenance Layer`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Cache Provenance Layer` boundaries where available and add only the missing `cache provenance schema, read filters, sync writes, and `cache reset --live` command` behavior.
Detailed Design: Add or change `cache provenance schema, read filters, sync writes, and `cache reset --live` command` so it satisfies `Add cache provenance support by migrating records to schema v8 with `provenance` constrained to `fixture` or `live`, writing provenance at sync commit time, exposing provenance through SourceRecord, SourceSummary, and SyncStatusResult, supporting provenance-filtered cache reads for CLI/MCP list/search/get surfaces, and implementing `cache reset --live` to delete only live-origin records.`. Keep ownership inside `Cache Provenance Layer`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: User runs fixture sync, live sync against httptest GitCode routes, MCP/CLI list/search/get, and `cache reset --live`; product outputs expose provenance or isolation state, fixture records remain after reset, live records are cleared, and `go test ./...` proves fixture-origin records cannot masquerade as live-origin records across the transition..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Cache Provenance Layer` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Cache Provenance Layer` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
