# Design Package Component: error-classifier

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Error Classifier

## Summary
The error classifier must convert every iteration-5 GitCode live failure into one canonical visible class: `api_validation`, `schema_decode`, `config_credential`, or `live_transport_failure`. The component-local work changes the diagnostics classifier, GitCode typed error metadata, service wrapper preservation, and CLI/MCP rendering tests so 400/schema/decode failures cannot surface as network or configuration outages.

## Top-Level Alignment
This component implements `a6`, resolves `task-7`, enforces `decommission-4`, and produces evidence for `outcome-7`, with supporting coverage for `outcome-1`, `outcome-3`, `outcome-5`, and `outcome-10`. It is affected by `error-classifier-delta-1`, whose required change type is `change`, so `Decision: detailed` is required.

## Tasks

### Task 1: Canonical codes and precedence
Outcome IDs: outcome-7, outcome-1, outcome-3, outcome-5, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-4
Change Type: change
Description: Replace the live-mode classification logic with an ordered canonical taxonomy inside the existing diagnostics classifier. The local role of this task is to make `internal/diagnostics` the single renderer-facing source of truth for live failure classes while preserving existing CLI and MCP error boundaries.
Existing Behavior / Reuse: Reuse `internal/diagnostics.Code`, `CommandContext`, `Classifier`, `Classify`, `classifyCode`, `codeFromError`, `hasCode`, `diagnosticCodes`, `messageFor`, `exitClassFor`, `httpAttemptedFor`, and `retryableFor`. The current classifier already has provider-mode-specific precedence and diagnostic-code unwrapping, but it lacks canonical `api_validation` and `schema_decode` outputs and still allows legacy live codes such as `live_api_failure`, `live_auth_failure`, and `unsupported_mock_payload` to leak through live HTTP mode. Reuse CLI `writeCommandError` and `diagnosticContext`, and extend MCP `writeDomainError` / `writeError` so live-origin domain errors use the same classifier.
Detailed Design: Add `CodeAPIFailure Code = "api_validation"` and `CodeSchemaDecode Code = "schema_decode"` to `internal/diagnostics`; keep legacy codes as accepted inputs but normalize them before live-mode rendering. Add `SchemaDecodeFailure`, `MalformedSuccess`, `LocalPayloadTooLarge`, and `FailureSource` fields to `CommandContext`; `FailureSource` values are `remote_status`, `local_body_limit`, `local_decode_boundary`, `partial_response`, `credential_resolver`, `transport`, or empty. Replace `classifyCode` for `ProviderMode: "live-http"` with ordered precedence: configuration invalidity, missing credential, invalid API base URL, local expired/bad credential without HTTP, remote 401/403 with HTTP attempted, schema/malformed 2xx decode, all non-auth 4xx API validation, local payload/partial/decode-boundary schema failure, then DNS/TCP/TLS/timeout/context-cancellation/5xx transport failure. A received HTTP 500 response, even with a body, is `live_transport_failure` unless an earlier explicit API-validation rule applies; this reconciles the architecture’s “5xx when not api_validation” rule by keeping rule 7 tightly limited to 4xx. Update `codeFromError`, `hasCode`, `messageFor`, `exitClassFor`, `httpAttemptedFor`, and `retryableFor` to recognize canonical codes and normalize `"live_api_failure"` to `api_validation`, `"unsupported_mock_payload"` to `schema_decode`, and `"live_auth_failure"` with HTTP attempted to `api_validation`. In MCP, route live-provider `gitcode.Err*`, `service.ErrSyncFailure`, and `service.ErrWriteFailure` through `diagnostics.Classify`; extend MCP error data with a visible `failure_class` value while keeping JSON-RPC structure stable. Decommission invariant: under live-http mode, no 400, malformed JSON, schema mismatch, partial response, or local body-limit path may return `live_transport_failure`, `configuration_error`, `live_api_failure`, `live_auth_failure`, or `unsupported_mock_payload` as the visible class.
Acceptance Criteria: Developer runs `go test ./internal/diagnostics/...`; table-driven runtime tests trigger configuration, missing credential, invalid API base URL, HTTP 401, HTTP 400, malformed 200 JSON, local body-limit, remote 413, partial response, timeout, and HTTP 500 cases, and each returns the expected canonical code. MCP and CLI product-path tests backed by mocked GitCode failures show visible `failure_class: api_validation`, `schema_decode`, `config_credential`, or `live_transport_failure`; regression tests fail if 400/schema/decode failures are rendered as transport or configuration failures.
Workload: 0.8 MM

### Task 2: DiagnosticCode on GitCode errors
Outcome IDs: outcome-7, outcome-1, outcome-3, outcome-5, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-4
Change Type: change
Description: Add diagnostic-code methods to GitCode typed errors that currently cannot participate in canonical classification through the existing unwrap chain. The local role of this task is to make GitCode live adapter errors self-describing without changing their error messages or public-safe text.
Existing Behavior / Reuse: Reuse existing `DiagnosticCode()` behavior on `gitcode.ErrNetworkUnavailable`, `gitcode.ErrAuthExpired`, `gitcode.ErrForbidden`, `gitcode.ErrValidationFailed`, and `gitcode.ErrFixtureReadOnly`. Reuse `diagnosticCodes` in `internal/diagnostics`, which already walks wrapped errors that implement `DiagnosticCode() string`. Confirmed missing component-local behavior: `gitcode.ErrNotFound`, `ErrConflict`, `ErrRemoteCollision`, `ErrRemoteNotFound`, `ErrPayloadTooLarge`, `ErrPartialResponse`, and `ErrRateLimited` do not provide the canonical extraction hook required by the iteration-5 classifier.
Detailed Design: Add `DiagnosticCode() string` on `gitcode.ErrNotFound` returning `"not_found"`, `ErrConflict` returning `"remote_conflict"`, `ErrRemoteCollision` returning `"remote_collision"`, `ErrRemoteNotFound` returning `"remote_not_found"`, `ErrPayloadTooLarge` returning `"payload_too_large"`, `ErrPartialResponse` returning `"partial_response"`, and `ErrRateLimited` returning `"rate_limited"`. The classifier maps not-found, conflict, remote-collision, remote-not-found, and rate-limited to `api_validation` when HTTP was attempted; partial-response maps to `schema_decode`; payload-too-large is resolved by `FailureSource` from Task 3. Do not change existing `Error()` text, redaction behavior, or non-live fixture behavior. Decommission invariant: these methods must not introduce legacy visible classes; they only feed the canonical classifier.
Acceptance Criteria: Developer runs `go test ./internal/diagnostics/...` with wrapped and unwrapped GitCode typed errors; `diagnosticCodes` extracts the expected string from each named type and `Classify` returns the canonical live class. Developer runs `go test ./internal/gitcode/...`; existing GitCode error-message behavior remains unchanged.
Workload: 0.3 MM

### Task 3: FailureSource in wrappers
Outcome IDs: outcome-7, outcome-1, outcome-3, outcome-5, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-4
Change Type: change
Description: Add concrete failure-source metadata so payload-size and partial-response errors classify differently depending on whether the failure came from GitCode status, local body limits, or decode boundaries. The local role of this task is to preserve source metadata through GitCode client errors, service sync/write wrappers, and CLI diagnostic context.
Existing Behavior / Reuse: Reuse `gitcode.ErrPayloadTooLarge`, `gitcode.ErrPartialResponse`, the GitCode HTTP client bounded-read path, `service.ErrSyncFailure`, `service.ErrWriteFailure`, `ErrSyncFailure.Unwrap`, `ErrSyncFailure.As`, and CLI `diagnosticContext`. Existing `ErrPayloadTooLarge` has endpoint, size, and limit data but no source distinction, so remote 413 and local body-limit enforcement can collapse into the wrong class. Existing service wrappers preserve causes but do not expose payload source metadata to the diagnostics command context.
Detailed Design: Add `Source string` and `FailureSource() string` to `gitcode.ErrPayloadTooLarge`; use `remote_status` for HTTP 413 or remote `ContentLength` over limit, and `local_body_limit` for client-side body reads exceeding `maxResponseSize`. Update the GitCode HTTP response path so HTTP 413 creates `ErrPayloadTooLarge{Source:"remote_status"}` instead of falling into a generic network/provider error. Add `PayloadSource string` to `service.ErrSyncFailure` and preserve `ErrPayloadTooLarge.Source` in sync normalization; keep `Cause` unwrap behavior so `errors.As` still reaches the underlying GitCode error. Extend CLI `diagnosticContext` to copy `PayloadSource` into `CommandContext.FailureSource`; MCP live error classification should either use the same wrapper metadata or classify the unwrapped GitCode error directly. Classification invariant: remote 413 is `api_validation`; local body-limit, local decode-boundary payload failure, and partial response after a successful HTTP response are `schema_decode`.
Acceptance Criteria: Developer runs `go test ./internal/diagnostics/...`; `ErrPayloadTooLarge{Source:"remote_status"}` with HTTP 413 returns `api_validation`, `ErrPayloadTooLarge{Source:"local_body_limit"}` with a successful/unknown status returns `schema_decode`, and `ErrPartialResponse` returns `schema_decode`. Developer runs service-level tests with a mocked GitCode client; `service.ErrSyncFailure` preserves `PayloadSource`, CLI diagnostic rendering uses it, and no payload-size case is rendered as `live_transport_failure` unless it is a true transport failure.
Workload: 0.5 MM

### Task 4: Product-path classifier tests
Outcome IDs: outcome-7, outcome-1, outcome-3, outcome-5, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-4
Change Type: change
Description: Add runtime tests that exercise canonical classification through real product error paths instead of only unit-level code mapping. The local role of this task is to prove CLI and MCP surfaces emit the same canonical classes after service wrapping.
Existing Behavior / Reuse: Reuse `internal/diagnostics` table-driven classifier tests, existing CLI command tests under the command entrypoint package, existing service wrapper tests, and MCP error rendering tests around `writeDomainError` / `writeError`. Existing tests cover basic classifier precedence but do not exercise every named GitCode typed error through `ErrSyncFailure`, `ErrWriteFailure`, CLI stderr rendering, and MCP JSON-RPC error data.
Detailed Design: Extend diagnostics tests with cases for `ErrNotFound`, `ErrConflict`, `ErrRemoteCollision`, `ErrRemoteNotFound`, `ErrPayloadTooLarge` with both sources, `ErrPartialResponse`, and `ErrRateLimited`, both direct and wrapped through `service.ErrSyncFailure` or `service.ErrWriteFailure`. Add CLI integration tests using an external-dependency-only `httptest.Server` or mocked GitCode client that returns 400, 401, 404, 409, 413, 429, malformed JSON, schema mismatch, partial response, timeout, and 500 scenarios; assert stderr includes the canonical `failure_class`. Add MCP error-rendering tests that trigger live-origin domain errors and assert JSON-RPC error data includes the same canonical `failure_class`. Decommission invariant: any product-path test that triggers 400, malformed JSON, schema mismatch, partial response, or local body-limit failure must explicitly fail if the visible class contains `live_transport_failure`, `configuration_error`, `live_api_failure`, `live_auth_failure`, or `unsupported_mock_payload`.
Acceptance Criteria: Developer runs `go test ./internal/diagnostics/...`, `go test ./internal/service/...`, `go test ./cmd/gitcode-mcp/...`, and `go test ./...` without credentials, network, SSH agent, or Keychain. CLI and MCP tests exercise target product surfaces with mocked GitCode responses and prove visible failure classes are exactly `api_validation`, `schema_decode`, `config_credential`, or `live_transport_failure`.
Workload: 0.6 MM

## Cross-Cutting Constraints
- Public-safe diagnostics — classifier messages and context must redact tokens, private coordinates, raw response bodies, and credentials through existing redaction filters because the repository must remain public-safe
- Offline validation — all classifier, CLI, MCP, and service tests use mocked GitCode responses or typed error structs; no real credentials, network, SSH agent, or Keychain are required
- Four-class taxonomy — iteration-5 live HTTP failures visibly resolve only to `api_validation`, `schema_decode`, `config_credential`, or `live_transport_failure`
- Decommission-4 invariant — 400/schema/decode failures are never reported as transport or configuration failures in live-http mode
- Wrapper preservation — `ErrSyncFailure` and `ErrWriteFailure` preserve underlying typed errors and failure-source metadata through unwrapping and diagnostic-code extraction

## Data And Control Flow
- GitCode HTTP response produces a typed `gitcode.Err*` error with `DiagnosticCode()` and optional `FailureSource()` metadata — live adapter/client owns the source classification input before service wrapping
- Service sync/write wraps the typed error as `service.ErrSyncFailure` or `service.ErrWriteFailure` while preserving cause, mode/code, and payload source metadata — service owns product-path propagation
- CLI `diagnosticContext` builds `diagnostics.CommandContext`, then `writeCommandError` calls `diagnostics.Classify` and renders canonical `failure_class` — CLI owns stderr presentation
- MCP `writeDomainError` calls `diagnostics.Classify` for live-origin errors and emits canonical `failure_class` in JSON-RPC error data — MCP owns client-facing error data
- `diagnostics.classifyCode` applies ordered live-http precedence; 4xx maps to `api_validation`, malformed/shape/local decode maps to `schema_decode`, credential-local failures map to `config_credential`, and transport/5xx maps to `live_transport_failure`

## Component Interactions
- `live_adapter` -> `error-classifier` — provides typed GitCode errors from live HTTP response handling; classifier determines canonical class after wrapping
- `gitcode.Err* typed errors` -> `error-classifier` — expose `DiagnosticCode()` and `FailureSource()` metadata consumed by `diagnosticCodes` and `classifyCode`
- `service.ErrSyncFailure / ErrWriteFailure` -> `error-classifier` — preserve cause, mode/code, and payload source so CLI/MCP diagnostics classify the original live failure instead of wrapper text
- `CLI` -> `error-classifier` — passes command context and renders canonical `failure_class` from `diagnostics.Classify`
- `MCP` -> `error-classifier` — routes live-origin domain errors through the same classifier and emits canonical JSON-RPC error data

## Rationale
The error classifier is materially affected because the approved architecture requires replacing the confusing iteration-4 live failure model with four visible classes. The existing diagnostics package has reusable classifier structure and error-code unwrapping, but it lacks canonical API/schema classes, failure-source metadata, and product-path regression coverage for wrapped GitCode live errors.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0481-run_attempt-1/final_message.txt`
