# Design Package Component: repository-binding

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Repository Binding

## Summary
Repository binding is detailed because the configured Component Impact delta requires it to own the live repository configuration contract. The design is limited to authoritative `api_base_url`, repository identity, cache/audit path handoff, and effective live-binding reporting for `sync --live` and `doctor --live`.

## Top-Level Alignment
This component supplies the selected repository binding values consumed by CLI startup, live provider construction, and doctor readiness. It preserves existing repo registry and scope validation while making repository binding `api_base_url` the authoritative live base URL when present.

## Tasks

### Task 1: Resolve Live Repository Binding
Outcome IDs: outcome-8, outcome-9
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: change
Description: Change the repository-binding contract so live commands resolve one effective repository binding snapshot containing repository identity, selected API base URL, cache path, and audit path. The local entity is the existing repository binding route/status model, extended so live-mode consumers do not independently choose between repository `api_base_url`, environment values, or defaults. The same effective snapshot is used by live provider startup and doctor readiness reporting.
Existing Behavior / Reuse: Reuse the existing `RepositoryBinding`, `AddRepositoryRequest`, `RepositoryStatus`, `RepositoryRoute`, repository registry storage, `normalizeRepositoryRequest`, and `BuildAdapterRoute` concepts. Existing behavior already stores and sanitizes `api_base_url` and validates issue/wiki scopes, but there is no explicit live-binding resolver that declares repository `api_base_url` authoritative, carries cache/audit path handoff metadata, or reports the exact effective values consumed by live mode. Existing non-live fixture behavior and scope validation remain unchanged.
Detailed Design: Add a repository-binding-owned effective live binding model, such as `LiveRepositoryBinding`, with fields for `RepoID`, `Owner`, `Name`, `APIBaseURL`, enabled `Scopes`, `CachePath`, `AuditPath`, and a non-secret `BaseURLSource` value whose live-mode value is `repository_binding` when the stored repository URL is present. Add a resolver function or service method, such as `ResolveLiveRepositoryBinding(ctx, repoID, requestedScope, runtimePaths)`, that first reuses `requireRepo` and repository lookup, then validates the requested scope through the same invariant as `BuildAdapterRoute`, then validates that the selected URL is absolute HTTP(S), normalized, credential-free, and non-empty for live mode. The resolver must return deterministic configuration diagnostics for missing repo, disabled scope, missing live URL, or invalid live URL before any live provider can be constructed; it must not read process-wide fallback URL values when the repository binding has `api_base_url`. Change `BuildAdapterRoute` only as needed to share normalization and scope validation internals, avoiding duplicate route-selection logic. Change `RepositoryStatus` or its live-readiness handoff so `doctor --live --format json` receives the same effective `APIBaseURL`, cache path, and audit path snapshot that live provider startup receives, with URL userinfo removed and no token material included. Update repository binding help/reference text only as part of this product change so `--api-base-url` is documented as the authoritative live base URL when configured, replacing any optional-default wording that contradicts the live-mode rule.
Acceptance Criteria: Operator configures repository binding `api_base_url` to mock server A, also configures a non-authoritative fallback URL to mock server B, and runs `gitcode-mcp sync --live`; the target product surface is the real CLI startup route through repository binding into the live provider, the visible/state outcome is requests hit only server A while server B receives zero requests or remains unused, and executable evidence is an offline Go CLI integration test using `httptest.Server` counters. Operator then runs `gitcode-mcp doctor --live --format json`; the target product surface is doctor JSON, the expected response includes provider mode `live-http`, credential source handoff, the effective cache path, and server A as `api_base_url`, and executable evidence is the same offline CLI integration test asserting sanitized JSON fields. Operator configures an invalid or missing repository live URL and runs `sync --live`; the target runtime route fails during repository-binding resolution with a configuration diagnostic before live HTTP construction, proven by a Go CLI test with zero mock-server requests.
Workload: 1.2 MM

## Cross-Cutting Constraints
- Repository binding `api_base_url` is authoritative for explicit live commands when present — prevents silent disagreement between global config, environment fallback, and per-repository mock routing
- Effective live readiness output must be public-safe — doctor consumes repository binding metadata but must not expose URL credentials, tokens, private paths beyond sanitized configured cache/audit paths, or raw fallback secrets
- Repository scope validation remains the binding gate — live issue/wiki operations must be rejected by repository binding before provider construction when the requested scope is disabled

## Data And Control Flow
- CLI startup requests live binding — `cli-startup` calls repository-binding live resolver with `repo_id`, requested scope, and runtime cache/audit paths — repository binding validates repo, scope, and selected URL before live provider construction
- Live provider receives selected route — `repository-binding` returns owner, name, scopes, and authoritative API base URL — live provider must use that URL exactly and must not re-resolve a different base URL
- Doctor reads effective snapshot — `doctor-readiness` consumes the same live binding snapshot — JSON reports effective API base URL and cache path rather than separately configured values

## Component Interactions
- `repository-binding` -> `cli-startup` — exposes `ResolveLiveRepositoryBinding`/effective live binding snapshot for live command composition and configuration diagnostics
- `repository-binding` -> `live-provider` — supplies owner, name, enabled scope, and authoritative `api_base_url`; provider owns HTTP behavior but not base URL selection
- `repository-binding` -> `doctor-readiness` — supplies non-secret effective repository binding fields for `doctor --live --format json`
- `repository-binding` -> `cache-runtime` — carries configured cache path ownership metadata so live sync/write and doctor agree on the runtime cache surface
- `repository-binding` -> `audit-runtime` — carries configured audit path ownership metadata for live write confirmation handoff

## Rationale
The approved architecture makes repository binding the authority for repository identity, cache/audit path binding, and live API base URL selection. Existing repo binding already stores `api_base_url`, so the smallest affected change is to promote that stored value into an explicit effective live binding contract consumed consistently by live startup and doctor readiness.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0231-run_attempt-1/final_message.txt`
