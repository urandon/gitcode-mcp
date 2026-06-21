# Design Package Component: internal-provider-live

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Live GitCode Adapter

## Summary
Live GitCode Adapter owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add Live GitCode REST API adapter package
Outcome IDs: outcome-3, outcome-4
Outcome Role: primary_product
Decommission IDs: decommission-4, decommission-5
Change Type: add
Description: Implement the `Live GitCode REST API adapter package` delta inside `Live GitCode Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Live GitCode Adapter` boundaries where available and add only the missing `Live GitCode REST API adapter package` behavior.
Detailed Design: Add or change `Live GitCode REST API adapter package` so it satisfies `Implement live provider with HTTP client, issue CRUD (list/get/create/update with pagination), comment list/create, wiki page CRUD (list/get/create/update with pagination), rate-limit handling (429), auth error handling (401/403), redacted diagnostics.`. Keep ownership inside `Live GitCode Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: sync --live → live provider fetches real issue/wiki/comment records from GitCode API. 429 response → rate-limit reported, clean exit. 401/403 → clear auth-failure diagnostic. create-issue --live with valid token → creates on remote..
Workload: 1 MM

### Task 2: Add Live sync: pagination, partial failure collect
Outcome IDs: outcome-3
Outcome Role: supporting_evidence
Decommission IDs: decommission-4
Change Type: add
Description: Implement the `Live sync: pagination, partial failure collection, delta computation` delta inside `Live GitCode Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Live GitCode Adapter` boundaries where available and add only the missing `Live sync: pagination, partial failure collection, delta computation` behavior.
Detailed Design: Add or change `Live sync: pagination, partial failure collection, delta computation` so it satisfies `Implement pagination via Link header/page-number iteration with configurable per-page (default 100); collect per-resource failures and report summary with success/failure counts; compute re-sync delta to avoid duplicating unchanged records.`. Keep ownership inside `Live GitCode Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: sync --live after previous sync → sync event reflects delta (or zero delta when nothing changed); unchanged records not duplicated. Partial failure (some pages fail) → summary reports success/failure counts, not crash..
Workload: 1 MM

### Task 3: Add Live write: idempotency gate and conflict dete
Outcome IDs: outcome-4
Outcome Role: supporting_evidence
Decommission IDs: decommission-5
Change Type: add
Description: Implement the `Live write: idempotency gate and conflict detection` delta inside `Live GitCode Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Live GitCode Adapter` boundaries where available and add only the missing `Live write: idempotency gate and conflict detection` behavior.
Detailed Design: Add or change `Live write: idempotency gate and conflict detection` so it satisfies `Implement idempotency key check before API call (via audit trail); dry-run mode (validate inputs, no remote call); conflict detection on 409 response; cache refresh after remote confirmation.`. Keep ownership inside `Live GitCode Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: create-issue --live with idempotency key → audit trail checked before API call. Duplicate key → 'already applied'. 409 response → conflict detected and reported. Dry-run → validates without API call..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Live GitCode Adapter` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Live GitCode Adapter` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
