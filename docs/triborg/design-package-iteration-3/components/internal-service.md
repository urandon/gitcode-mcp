# Design Package Component: internal-service

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Service Factory

## Summary
Extend the `internal/service` factory so `Service` can be constructed with fixture, live, or unavailable provider mode while preserving the existing fixture-only default path. The component-local change replaces the hard-wired `sanitizedFixtureClient` in the default construction path with explicit mode selection for the existing `gitcode.Client` field.

## Top-Level Alignment
`internal/service/` implements the service-factory portion of Task 1: it accepts the provider mode selected by `cmd/gitcode-mcp/` and wires the `Service` with the correct `gitcode.Client`. Fixture mode remains the default so offline tests and non-live commands stay deterministic.

## Tasks

### Task 1: Service factory selects provider mode
Outcome IDs: outcome-1
Outcome Role: primary_product
Decommission IDs: decommission-1
Change Type: change
Description: Change the existing `Service` factory so provider selection is explicit instead of always constructing the embedded fixture client. The existing `Service` struct remains the runtime owner of `cache.Store` and the `gitcode.Client` field, while CLI-owned credential resolution is handed to this factory as an already-resolved credential result. This task removes the hard-wired `sanitizedFixtureClient` as the only production construction path while preserving it for fixture mode.
Existing Behavior / Reuse: Reuse the existing `Service` struct in `internal/service`, its existing `store cache.Store` dependency, its existing `client gitcode.Client` field, and the existing `sanitizedFixtureClient` type as the fixture-mode implementation. Reuse the existing `gitcode.Client` interface, existing `gitcode.HTTPClient`, existing `gitcode.Config`, existing `gitcode.ProviderMode` constants where available, and existing `gitcode.ErrProviderUnavailable` for unavailable live construction. Keep `NewWithClient` as the test and custom-client escape hatch; it should not be removed or repurposed.
Detailed Design: Add a mode-aware constructor, named consistently with existing service constructors, such as `NewWithMode(store cache.Store, mode gitcode.ProviderMode, credential *credential.Result, cfg ServiceConfig) (*Service, error)`. `ServiceConfig` carries only non-secret HTTP construction settings such as base URL, timeout, max response size, max retries, user agent, and pagination defaults; it must not expose a raw `token string` parameter. `cmd/gitcode-mcp/` owns the credential pipeline through `internal/credential/`, resolves env/keychain/none, and passes the resulting credential object to this service constructor; the constructor extracts the token only to populate `gitcode.Config` for `gitcode.NewHTTPClient` and never logs or stores token previews.

For `gitcode.ProviderModeFixture`, the constructor creates `Service{store: store, client: sanitizedFixtureClient{}, ...}` and sets a `providerMode` field to fixture; this path is invariantly offline, ignores live credentials, and never returns an error. For `gitcode.ProviderModeLive`, the constructor requires a non-nil credential result with a usable token, maps `ServiceConfig` into `gitcode.Config`, constructs `gitcode.HTTPClient` through the existing `gitcode.NewHTTPClient`, stores it in the existing `Service.client gitcode.Client` field, and records `providerMode` as live. For `gitcode.ProviderModeUnavailable` or live mode with no usable credential/config, the constructor returns an error wrapping or containing `gitcode.ErrProviderUnavailable` immediately; do not add an `unavailableServiceClient` stub because construction-time failure is simpler and prevents lazy runtime surprises. Preserve `New(store cache.Store) *Service` as the backward-compatible fixture default by making it delegate with `gitcode.ProviderModeFixture` or retain a direct fixture construction path; because fixture mode never errors, `New` must never panic from unavailable-mode selection.

Add `ProviderMode() gitcode.ProviderMode` on `Service` for doctor/readiness introspection. Update `NewWithClient(store cache.Store, client gitcode.Client)` to set `providerMode` to fixture or an explicit custom/test value without changing its injected-client semantics. The negative invariant for `decommission-1` is that `sanitizedFixtureClient` is no longer the only provider path reachable from the service factory; it remains an internal fixture implementation used only by fixture/default and selected tests.
Acceptance Criteria: A service-layer Go test constructs `New(store)` with an in-memory or temporary `cache.Store`, calls `SyncToCache`, and observes fixture records without network access; executable evidence is `go test ./internal/service` and default `go test ./...` with no live env vars. A service-layer Go test constructs `NewWithMode(store, gitcode.ProviderModeFixture, nil, ServiceConfig{})`, calls a sync path, and observes the same fixture-backed state with no error and `ProviderMode()` returning fixture. A service-layer Go test constructs `NewWithMode(store, gitcode.ProviderModeLive, resolvedCredential, ServiceConfig{...})` against an external-provider test server or local HTTP test endpoint, calls a read/sync path, and verifies the existing `gitcode.HTTPClient` path is used rather than `sanitizedFixtureClient`. A service-layer Go test constructs `NewWithMode(store, gitcode.ProviderModeUnavailable, nil, ServiceConfig{})` or live mode with no credential and verifies construction fails with an error recognized as `gitcode.ErrProviderUnavailable` / `gitcode.IsProviderUnavailable`, with no `Service` returned.
Workload: 0.5 MM

## Cross-Cutting Constraints
- `Service` consumes `gitcode.Client`; it does not own credential acquisition or CLI flag parsing — this keeps provider selection aligned with the CLI → credential → service-factory architecture
- Fixture mode must remain deterministic and offline for `go test ./...` — the default constructor must always choose fixture mode and never inspect live env vars
- Raw credentials must not appear as service-factory parameters, diagnostics, logs, or stored fields beyond the existing HTTP client configuration needed to issue authenticated requests

## Data And Control Flow
- `cmd/gitcode-mcp/` resolves `--live` and credentials, then calls the mode-aware service constructor with `cache.Store`, provider mode, resolved credential result, and non-secret service config — service construction happens once at command start
- `internal/service` maps fixture mode to `sanitizedFixtureClient`, live mode to existing `gitcode.HTTPClient`, and unavailable/missing-credential mode to an immediate `gitcode.ErrProviderUnavailable` construction error — no dynamic provider switching occurs after construction
- Existing service methods continue to use the existing `Service.client gitcode.Client` field for sync and write operations and the existing `Service.store cache.Store` field for cache-backed behavior — provider choice is isolated to construction

## Component Interactions
- `cmd/gitcode-mcp/` -> `internal/service/` — passes selected `gitcode.ProviderMode`, resolved `credential.Result`, and non-secret service config; it does not pass a raw token string
- `internal/service/` -> `internal/gitcode/` — constructs the existing `gitcode.HTTPClient` for live mode and returns existing provider-unavailable errors for unavailable mode
- `internal/service/` -> `internal/doctor/` — exposes `ProviderMode()` so readiness reporting can describe the constructed mode without inspecting secrets
- `internal/service/` -> `internal/search/` — service remains a consumer/delegator for search behavior; source-search routing fixes are owned by `internal/search/`, not this component

## Rationale
This component is affected because the existing service factory owns the construction of `Service.client`, and the current hard-wired fixture client blocks live provider selection. The single component-impact delta for `internal-service` is fully covered by making provider mode an explicit constructor input while keeping default fixture behavior intact.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-1808-run_attempt-1/final_message.txt`
