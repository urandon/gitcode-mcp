# Design Package Component: internal-auth

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Credential Resolver

## Summary
Credential Resolver owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add Shared CredentialResolver struct (internal/aut
Outcome IDs: outcome-6
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Implement the `Shared CredentialResolver struct (internal/auth/resolver.go)` delta inside `Credential Resolver`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Credential Resolver` boundaries where available and add only the missing `Shared CredentialResolver struct (internal/auth/resolver.go)` behavior.
Detailed Design: Add or change `Shared CredentialResolver struct (internal/auth/resolver.go)` so it satisfies `Design shared CredentialResolver struct with env var GITCODE_TOKEN > basic auth env vars GITCODE_USER/GITCODE_PASS > OS keychain priority order. Invoked once per command. Resolve() returns Credential (token or basic auth). Result deterministically passed to all downstream paths: auth status, read probes, sync, and write commands. Graceful fallback when keychain unavailable.`. Keep ownership inside `Credential Resolver`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: GITCODE_TOKEN env var set: auth status reports credential present; add-comment --live includes bearer token in outbound HTTP request. No credential: auth status reports credential_unavailable; create-issue --live fails with credential_unavailable before outbound HTTP call. Multiple sources (env var + keychain): same source picked for both auth status and write command..
Workload: 1 MM

### Task 2: Validate Credential resolver parity tests (interna
Outcome IDs: outcome-6
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: validate
Description: Implement the `Credential resolver parity tests (internal/auth/resolver_test.go)` delta inside `Credential Resolver`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `Credential Resolver` boundaries where available and add only the missing `Credential resolver parity tests (internal/auth/resolver_test.go)` behavior.
Detailed Design: Add or change `Credential resolver parity tests (internal/auth/resolver_test.go)` so it satisfies `Write mocked tests: GITCODE_TOKEN env var -> auth status reports present, add-comment includes bearer token. No credential -> auth status credential_unavailable, create-issue fails credential_unavailable before HTTP call. Multi-source (env + keychain) -> same source picked for auth status and write. Priority order documented in test.`. Keep ownership inside `Credential Resolver`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: go test ./internal/auth/... passes. All credential resolution scenarios verified. Resolver invoked once per command; result deterministically passed to all paths. Priority order env var > basic auth > keychain confirmed by test assertions..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `Credential Resolver` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `Credential Resolver` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
