# Design Package Component: internal-credential

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Credential Pipeline

## Summary
Credential Pipeline owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add Credential pipeline with env→keychain→none fal
Outcome IDs: outcome-2
Outcome Role: primary_product
Decommission IDs: decommission-3
Change Type: add
Description: Implement the `Credential pipeline with env→keychain→none fallback chain` delta inside `Credential Pipeline`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Credential Pipeline` boundaries where available and add only the missing `Credential pipeline with env→keychain→none fallback chain` behavior.
Detailed Design: Add or change `Credential pipeline with env→keychain→none fallback chain` so it satisfies `Implement credential resolver: check GITCODE_TOKEN env var first; fall back to macOS Keychain (darwin build-tag gated, runtime-detected); fall through to none. Track CredentialSource enum for reporting.`. Keep ownership inside `Credential Pipeline`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: GITCODE_TOKEN set → resolver returns env source with token. No GITCODE_TOKEN, keychain present on darwin → returns keychain source with token. Neither available → returns none. go test ./... passes without keychain dependency..
Workload: 1 MM

### Task 2: Add Auth status reporting and invalid-token diagno
Outcome IDs: outcome-2
Outcome Role: supporting_evidence
Decommission IDs: decommission-3
Change Type: add
Description: Implement the `Auth status reporting and invalid-token diagnostics` delta inside `Credential Pipeline`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Credential Pipeline` boundaries where available and add only the missing `Auth status reporting and invalid-token diagnostics` behavior.
Detailed Design: Add or change `Auth status reporting and invalid-token diagnostics` so it satisfies `Implement auth status reporting: token source identification, redacted token preview, available-source listing when none found. Provide clear auth-failure diagnostic (not generic HTTP error) when invalid token used against live API.`. Keep ownership inside `Credential Pipeline`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: auth status with token → reports source (env/keychain) + redacted value. auth status without token → lists available token source options. GITCODE_TOKEN=invalid sync --live → clear auth-failure diagnostic, not generic HTTP error..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Credential Pipeline` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Credential Pipeline` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
