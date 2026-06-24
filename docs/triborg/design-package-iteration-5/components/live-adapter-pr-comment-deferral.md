# Design Package Component: live-adapter-pr-comment-deferral

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Live Adapter PR/Comment Deferral

## Summary
Live Adapter PR/Comment Deferral owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Change live adapter PR/comment deferred surface ha
Outcome IDs: outcome-5, outcome-1, outcome-7, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-4
Change Type: change
Description: Implement the `live adapter PR/comment deferred surface handlers` delta inside `Live Adapter PR/Comment Deferral`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Live Adapter PR/Comment Deferral` boundaries where available and add only the missing `live adapter PR/comment deferred surface handlers` behavior.
Detailed Design: Add or change `live adapter PR/comment deferred surface handlers` so it satisfies `Route Pull Request and comment read/sync attempts through explicit deferred-surface handlers keyed by the RouteSchemaMatrix, returning `unsupported_capability` diagnostics for `pull_requests_read` and `comments_read` without issuing unsupported live HTTP calls or treating deferral as empty cache success or transport failure.`. Keep ownership inside `Live Adapter PR/Comment Deferral`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: User triggers production CLI/MCP PR and comment read/sync paths under mocked live-provider tests; `go test ./...` asserts the response is `unsupported_capability` with the configured diagnostic, no silent empty-cache result is returned, and no `live_transport_failure` is reported for the deferred surface..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Live Adapter PR/Comment Deferral` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Live Adapter PR/Comment Deferral` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
