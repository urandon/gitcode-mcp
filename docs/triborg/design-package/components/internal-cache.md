# Design Package Component: internal-cache

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Cache Storage

## Summary
Cache Storage owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add Schema version detection and compatibility che
Outcome IDs: outcome-10
Outcome Role: primary_product
Decommission IDs: decommission-10
Change Type: add
Description: Implement the `Schema version detection and compatibility check on cache open` delta inside `Cache Storage`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Cache Storage` boundaries where available and add only the missing `Schema version detection and compatibility check on cache open` behavior.
Detailed Design: Add or change `Schema version detection and compatibility check on cache open` so it satisfies `Query PRAGMA user_version on cache open; compare against expected schema version 3; implement Cache Open Predicate: version 3 → normal open; version 2 → warn, block writes, suggest migrate-cache; version 1 → block, report incompatibility, suggest reinit-cache; version >3 → block, recommend binary upgrade.`. Keep ownership inside `Cache Storage`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Iter-2 cache opened with iter-3 binary → schema version mismatch reported with detected and expected version, actionable 'migrate-cache' message. Iter-1 cache → incompatibility reported, re-initialization recommended. Future version cache → binary downgrade not supported..
Workload: 1 MM

### Task 2: Add Version-2-to-version-3 cache schema migration
Outcome IDs: outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-10
Change Type: add
Description: Implement the `Version-2-to-version-3 cache schema migration` delta inside `Cache Storage`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Cache Storage` boundaries where available and add only the missing `Version-2-to-version-3 cache schema migration` behavior.
Detailed Design: Add or change `Version-2-to-version-3 cache schema migration` so it satisfies `Implement transaction-wrapped schema migration from version 2 to version 3 that upgrades schema in place without data loss; include backup prompt before migration; set PRAGMA user_version to 3 on success.`. Keep ownership inside `Cache Storage`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: gitcode-mcp migrate-cache against iter-2 cache → schema upgraded in place, data preserved, PRAGMA user_version set to 3. gitcode-mcp migrate-cache against iter-1 cache → incompatibility, no migration attempted..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Cache Storage` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Cache Storage` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
