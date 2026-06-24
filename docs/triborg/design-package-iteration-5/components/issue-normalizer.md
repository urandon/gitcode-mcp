# Design Package Component: issue-normalizer

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Issue Normalizer

## Summary
Issue Normalizer owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Change live issue response identity decoder and ca
Outcome IDs: outcome-2, outcome-7, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-4
Change Type: change
Description: Implement the `live issue response identity decoder and cache identity mapper` delta inside `Issue Normalizer`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Issue Normalizer` boundaries where available and add only the missing `live issue response identity decoder and cache identity mapper` behavior.
Detailed Design: Add or change `live issue response identity decoder and cache identity mapper` so it satisfies `Replace the narrow mocked-shape issue decoder in the live adapter response path with a normalizer that accepts GitCode numeric `id` and string `number`, stores stable `remote_id` and `source_id` values, and emits field-level `schema_decode` diagnostics for missing or malformed identity fields.`. Keep ownership inside `Issue Normalizer`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Sync actor runs mocked live issue reads through `GET /api/v5/repos/{owner}/{repo}/issues` using production adapter code; numeric `id` and string `number` responses are cached and readable through CLI or MCP with stable source identifiers, while malformed identity fixtures produce `schema_decode` diagnostics under `go test ./...`..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Issue Normalizer` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Issue Normalizer` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
