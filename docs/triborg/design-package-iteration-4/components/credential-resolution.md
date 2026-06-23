# Design Package Component: credential-resolution

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Credential Resolution

## Summary
Credential resolution needs one shared live credential contract for CLI live startup, sync admission, write gating, auth status, live provider construction, and doctor reporting. The component change is narrow: reuse existing credential provider concepts while replacing command-local token checks with a single `CredentialResolutionResult`.

## Top-Level Alignment
This component implements the approved architecture’s shared credential pipeline for Task 2, Task 3, and Task 9. It returns typed `missing_credential` before live HTTP, supplies provider-consumable secrets only to live command consumers, and exposes non-secret source metadata for doctor readiness.

## Tasks

### Task 1: Resolver result unifies live credentials
Outcome IDs: outcome-2, outcome-3, outcome-9
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: change
Description: The credential-resolution component must provide the single authority for live credential presence, provider token material, and non-secret source metadata. The resolver owns the distinction between no usable credential and a present credential that may later fail live authentication. Auth status, sync/live startup, write gates, doctor readiness, and provider construction must consume this same result instead of performing separate source checks.
Existing Behavior / Reuse: Reuse existing `CredentialProvider`, `ChainCredentialProvider`, `EnvCredentialProvider`, `KeychainCredentialProvider`, `StaticCredentialProvider`, `CredentialStatus`, and `SecretString` concepts. Confirmed absent: there is no single live-focused `CredentialResolutionResult` consumed by all live CLI paths; write gating still has direct environment fallback behavior, and the newer credential pipeline/status concepts are not the authoritative live CLI credential contract. Existing provider chaining and redaction behavior should be kept, while direct `GITCODE_TOKEN` reads and command-local credential fallbacks in write gates, auth status, sync startup, and provider construction are replaced by resolver consumption.
Detailed Design: Add or formalize a live resolver entrypoint, conceptually `ResolveLiveCredential(ctx, effectiveConfig)`, returning `CredentialResolutionResult` or a typed `MissingCredentialError` whose diagnostic code is exactly `missing_credential`. `CredentialResolutionResult` contains `Present`, provider-only `SecretString`, `Source`, `StoreMode`, `AttemptedSources`, `AvailableSources`, `UnavailableSources`, `ErrorClass`, and `Remediation`; doctor/auth status may serialize only non-secret metadata and readiness fields, never the token. Provider order is environment token first, then explicitly injected static/mock Keychain-equivalent provider when configured, then production Keychain only when allowed by effective credential policy; the first non-empty trimmed token wins. Empty or whitespace-only tokens are treated as absent for that source and recorded in metadata without secret values; provider lookup errors and unavailable Keychain sources are recorded as non-secret source status and do not become `missing_credential` until every allowed source fails to produce a non-empty token. Any non-empty resolved token, including an invalid, malformed, expired, or scope-insufficient token, is `Present=true`; credential-resolution must not validate token authenticity or scope, and live HTTP 401/403 remains owned by `live-provider` as `live_auth_failure`. Replace external direct credential paths with this invariant: sync/live startup gates on `CredentialResolutionResult.Present`, live provider construction receives only `SecretString` from the result, write-service gates on the same resolved-present state, auth status reports the same source metadata, and callers must not independently read `GITCODE_TOKEN`, invoke Keychain, or apply fallback token logic. Mocked Keychain-equivalent/static providers are supplied only through test/config injection seams, are not persisted into repository config, expose only source metadata such as `mock-keychain`, and primary offline tests must configure credential policy so OS Keychain is not called.
Acceptance Criteria: Operator runs `gitcode-mcp sync --live` with no `GITCODE_TOKEN` and no injected credential source; target surface is the real live CLI startup route, expected result is typed `missing_credential` failure with zero mock-server requests, and executable evidence is an offline CLI integration test under `go test ./...`. Operator runs `gitcode-mcp create-issue --live` with `GITCODE_TOKEN` unset and an injected mocked Keychain-equivalent provider; target surface is the live write route, expected result is an authenticated mock create request using the shared resolved credential, and executable evidence is a stubbed-external-provider CLI integration test. Operator runs `gitcode-mcp doctor --live --format json` with the same injected credential; target surface is doctor JSON, expected result reports provider mode and credential source metadata without token material, and executable evidence is a local JSON assertion under `go test ./...`.
Workload: 0.8 MM

## Cross-Cutting Constraints
- Secret values remain provider-only data — doctor, diagnostics, auth status, audit, and cache consumers receive source metadata and redacted status only
- Missing credentials are classified before live HTTP — live startup, sync admission, and write gates must use the resolver result before constructing or invoking the live provider
- Present tokens are not authenticated by credential-resolution — non-empty invalid credentials flow to live provider so 401/403 can be classified as `live_auth_failure` by the live-provider component
- Test credential injection preserves production semantics — mocked Keychain-equivalent/static providers use the provider chain contract through test/config seams and primary tests do not call OS Keychain

## Data And Control Flow
- Live command startup requests `CredentialResolutionResult` — credential-resolution returns provider-only secret plus metadata or typed `missing_credential` — startup admits or rejects live provider construction before HTTP
- Sync live startup consumes the same `CredentialResolutionResult.Present` and metadata contract — sync does not read environment variables or Keychain directly — fixture fallback cannot be selected due to local credential checks
- Live create issue requests the same resolver result — write-service consumes resolved-present state and provider token — authenticated mock write proves auth status and write path share the source contract
- Auth status requests resolver metadata — credential-resolution reports attempted, available, unavailable, and selected sources without secret values — auth status does not run separate source checks
- Doctor requests resolver metadata — credential-resolution strips secret material and reports source/store/readiness fields — doctor serializes effective live readiness JSON

## Component Interactions
- `credential-resolution` -> `cli-startup` — returns `CredentialResolutionResult` or typed `missing_credential` before live provider construction; `cli-startup` must not independently resolve tokens
- `credential-resolution` -> `sync-service` — supplies the same presence and metadata contract used by live startup for `sync --live`, replacing direct environment or command-local fallback checks
- `credential-resolution` -> `live-provider` — supplies provider-only `SecretString` after a source resolves a non-empty token; invalid-token HTTP outcomes remain live-provider diagnostics
- `credential-resolution` -> `write-service` — supplies the resolved-present credential state used by live provider construction, replacing direct `GITCODE_TOKEN` fallback in write gates
- `credential-resolution` -> `auth status` — supplies `CredentialResolutionResult` metadata and presence status so auth status cannot use a separate source path from sync/write
- `credential-resolution` -> `doctor-readiness` — supplies non-secret source metadata, attempted/available source lists, store mode, and missing-credential readiness fields

## Rationale
Credential-resolution is affected because live readiness depends on one shared credential contract across auth status, sync, write, provider construction, and doctor output. Without this change, missing-credential diagnostics, mocked Keychain-equivalent writes, invalid-token live failures, and doctor readiness can diverge even when live provider wiring is otherwise correct.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0357-run_attempt-1/final_message.txt`
