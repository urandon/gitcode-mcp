# Design Package Component: internal-doctor

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Doctor Aggregator

## Summary
Doctor Aggregator owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add Doctor aggregator with subsystem introspection
Outcome IDs: outcome-11
Outcome Role: primary_product
Decommission IDs: decommission-11
Change Type: add
Description: Implement the `Doctor aggregator with subsystem introspection and public-safe report formatting` delta inside `Doctor Aggregator`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Doctor Aggregator` boundaries where available and add only the missing `Doctor aggregator with subsystem introspection and public-safe report formatting` behavior.
Detailed Design: Add or change `Doctor aggregator with subsystem introspection and public-safe report formatting` so it satisfies `Implement doctor aggregator that queries config, credential, cache, sync, index, and MCP subsystems for readiness state; format a public-safe report with version, config path, cache dir, schema version, repo binding, token source (redacted), live provider reachability, auth probe, last sync timestamp, index freshness, MCP transport status, and actionable diagnostics for missing binding/token.`. Keep ownership inside `Doctor Aggregator`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: doctor reports all readiness dimensions (version, config, cache, repo, token, live provider, auth probe, sync, index, MCP transport) — all public-safe. No binding → 'no repo bound' + bind suggestion. No token → 'no token configured' + available sources..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Doctor Aggregator` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Doctor Aggregator` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
