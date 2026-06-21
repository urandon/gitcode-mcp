# Design Package Component: internal-provider

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Provider Interface and Dispatch

## Summary
Provider Interface and Dispatch owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add ProviderMode enum and provider factory dispatc
Outcome IDs: outcome-1
Outcome Role: primary_product
Decommission IDs: decommission-1, decommission-2
Change Type: add
Description: Implement the `ProviderMode enum and provider factory dispatch` delta inside `Provider Interface and Dispatch`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Provider Interface and Dispatch` boundaries where available and add only the missing `ProviderMode enum and provider factory dispatch` behavior.
Detailed Design: Add or change `ProviderMode enum and provider factory dispatch` so it satisfies `Define ProviderMode enum (fixture/live/unavailable); implement factory dispatch that returns correct provider based on mode; preserve existing fixture provider for default path.`. Keep ownership inside `Provider Interface and Dispatch`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: ProviderMode enum has fixture/live/unavailable variants. Factory dispatch returns fixture provider when mode=fixture, live provider when mode=live, unavailable provider when mode=unavailable. go test ./... uses fixture path..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Provider Interface and Dispatch` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Provider Interface and Dispatch` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
