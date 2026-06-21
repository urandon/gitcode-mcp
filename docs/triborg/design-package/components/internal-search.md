# Design Package Component: internal-search

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Search Engine

## Summary
Search Engine owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Change search_sources FTS query routing and empty-
Outcome IDs: outcome-7
Outcome Role: primary_product
Decommission IDs: decommission-7
Change Type: change
Description: Implement the `search_sources FTS query routing and empty-set behavior` delta inside `Search Engine`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Search Engine` boundaries where available and add only the missing `search_sources FTS query routing and empty-set behavior` behavior.
Detailed Design: Add or change `search_sources FTS query routing and empty-set behavior` so it satisfies `Rewire search_sources to query the same cache/FTS backend as search_chunks; return empty result set on no match instead of cache_empty error; auto-build FTS index on first search after sync if not yet built.`. Keep ownership inside `Search Engine`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: search_sources 'test' after fixture sync/index → non-empty results. search_sources 'NONEXISTENT' → empty result set, no error. MCP search_sources tool returns correct source records..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Search Engine` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Search Engine` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
