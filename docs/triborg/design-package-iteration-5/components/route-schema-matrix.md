# Design Package Component: route-schema-matrix

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: RouteSchemaMatrix

## Summary
RouteSchemaMatrix is a new static coverage artifact for GitCode live API iteration 5. It declares the supported, deferred, or unsupported status for each live product surface and provides the evidence class and diagnostic contract consumed by the live adapter at construction or preflight.

## Top-Level Alignment
This component implements the architecture’s coverage contract for Issues, Labels, Milestones, Pull Requests, Comments, and Wiki. It centralizes route/schema decisions while preserving the invariant that the matrix is configuration, not an HTTP dispatch tree.

## Tasks

### Task 1: Add RouteSchemaMatrix contract
Outcome IDs: outcome-1, outcome-5, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: add
Description: Add the RouteSchemaMatrix static coverage artifact owned by the live GitCode provider boundary. The matrix records each product area’s support status, `/api/v5` route family, evidence class, and diagnostic payload so the live adapter can reject deferred PR/comment surfaces with typed unsupported diagnostics. The component also provides construction-time validation so missing or contradictory surface declarations cannot silently fall back to empty cache reads or transport-class failures.
Existing Behavior / Reuse: Existing live provider construction is exposed through the `internal/provider/live` adapter package and delegated into the project-owned GitCode provider construction code. Existing endpoint/path helpers in the GitCode provider build concrete `/api/v5` paths directly, and existing provider error/diagnostic concepts expose diagnostic codes through typed errors and classifier-facing values. Repository inspection confirms no existing `RouteSchemaMatrix`, `ProductArea`, `SupportStatus`, `EvidenceClass`, central coverage contract, or matrix-sourced `unsupported_capability` preflight exists; reuse the live provider construction boundary, endpoint helper concept, provider error diagnostic-code pattern, and adapter-level tests rather than duplicating HTTP route execution.
Detailed Design: Add component-local data types `RouteSchemaMatrix`, `ProductArea`, `SupportStatus`, `EvidenceClass`, `RouteFamily`, `SurfaceSpec`, and `UnsupportedDiagnostic` in the GitCode live provider/model boundary. `ProductArea` must enumerate exactly `issues`, `labels`, `milestones`, `pull_requests`, `comments`, and `wiki`; `SupportStatus` must enumerate `supported`, `deferred`, and `unsupported`; `EvidenceClass` must enumerate `OpenAPI`, `live_probe`, and `deferred`; `RouteFamily` must validate to `/api/v5` for every iteration-5 surface. Add `DefaultRouteSchemaMatrix()` returning these exact immutable entries: `issues` = `supported`, route family `/api/v5`, evidence `OpenAPI`, no unsupported diagnostic; `labels` = `supported`, route family `/api/v5`, evidence `OpenAPI`, no unsupported diagnostic; `milestones` = `supported`, route family `/api/v5`, evidence `OpenAPI`, no unsupported diagnostic; `wiki` = `supported`, route family `/api/v5`, evidence `live_probe`, no unsupported diagnostic; `pull_requests` = `deferred`, route family `/api/v5`, evidence `deferred`, diagnostic code `unsupported_capability`, capability key `pull_requests_read`, operator message indicating Pull Request reads are deferred for iteration 5 pending live shape validation; `comments` = `deferred`, route family `/api/v5`, evidence `deferred`, diagnostic code `unsupported_capability`, capability key `comments_read`, operator message indicating comment reads are deferred for iteration 5 pending child-record shape validation. Add lookup and validation methods such as `Spec(area ProductArea) (SurfaceSpec, bool)`, `RequireDeclared(area ProductArea) error`, and `ValidateCoverage(required []ProductArea) error`; validation must reject missing required product areas, unknown product-area values, unknown support statuses, unknown evidence classes, empty route families, any non-`/api/v5` route family, supported specs using `deferred` evidence, deferred/unsupported specs using non-`deferred` evidence, deferred/unsupported specs without diagnostic code `unsupported_capability`, and deferred/unsupported specs without a non-empty capability key and message. Expose a provider construction contract such as `WithRouteSchemaMatrix(matrix RouteSchemaMatrix)` or an equivalent option accepted by the live adapter/provider construction area; when omitted, construction uses `DefaultRouteSchemaMatrix()` and validates it before returning the provider. Add a matrix preflight method used by the live adapter before PR/comment product reads; supported surfaces continue into the existing endpoint/path helpers and HTTP adapter code, while deferred PR/comment surfaces return the matrix-sourced unsupported diagnostic before any outbound HTTP request is attempted and remain distinguishable from `api_validation`, `schema_decode`, `config_credential`, and `live_transport_failure`.
Acceptance Criteria: Developer runs `go test ./...` without credentials, network, SSH agent, or OS Keychain; adapter-level tests construct the production live provider with `DefaultRouteSchemaMatrix()` and trigger issue, label, milestone, PR, comment, and wiki product surfaces against `httptest` GitCode routes. Issue, label, milestone, and wiki supported surfaces continue to the appropriate mocked `/api/v5` route family and either parse GitCode-shaped responses or hand off to their normal parser, while PR/comment read attempts return visible `unsupported_capability` diagnostics with capability keys `pull_requests_read` and `comments_read`, no empty success result, no outbound HTTP request, and no `live_transport_failure`. Matrix validation tests mutate the default matrix to remove a required product area, use an unknown enum value, use a non-`/api/v5` route family, mark a supported spec with `deferred` evidence, and mark a deferred spec without `unsupported_capability`; each case fails provider construction or preflight deterministically before any network call.
Workload: 0.8 MM

## Cross-Cutting Constraints
- The matrix must remain public-safe and contain only generic route families, evidence classes, status values, capability keys, and sanitized diagnostics — this prevents credentials, cookies, private coordinates, or raw API captures from entering durable source artifacts
- Deferred surfaces must fail with explicit unsupported capability diagnostics rather than empty success or transport-class errors — this preserves the approved PR/comment deferral behavior across CLI and MCP consumers
- Offline tests must exercise product runtime paths with mocked external GitCode routes only — this keeps normal validation independent of live network, credentials, SSH agent, and OS Keychain

## Data And Control Flow
- Live provider construction receives the default or supplied RouteSchemaMatrix — live adapter/provider construction — validation occurs before product surfaces are used
- Product surface preflight asks the matrix for the declared `SurfaceSpec` — RouteSchemaMatrix to live adapter — supported surfaces continue to adapter route execution, while deferred surfaces return the matrix diagnostic before HTTP
- PR/comment read attempts resolve to deferred specs — RouteSchemaMatrix to CLI/MCP-facing adapter behavior — `unsupported_capability` is returned without outbound GitCode traffic

## Component Interactions
- `RouteSchemaMatrix` -> `live_adapter` — Provides immutable per-product-area `SurfaceSpec` declarations and validation methods consumed during live adapter construction or surface preflight
- `RouteSchemaMatrix` -> `test_suite` — Provides the default coverage contract used by adapter-level `httptest` scenarios for supported and deferred surfaces
- `RouteSchemaMatrix` -> `cli_mcp_pr_comment_surface` — Supplies the deferred PR/comment diagnostic payload that CLI/MCP surfaces expose without classifying the attempt as transport failure

## Rationale
The configured component is affected because the approved architecture introduces RouteSchemaMatrix as the central coverage contract for live GitCode API iteration 5. Existing live provider construction and endpoint helpers are present, but no central matrix artifact or construction-time coverage validation exists, so the component-local delta is concrete and missing.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0297-run_attempt-1/final_message.txt`
