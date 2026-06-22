# Design Package Component: live-provider

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Live Provider

## Summary
The live-provider component is affected because explicit live CLI routes depend on it to perform authenticated GitCode-compatible HTTP reads and writes against the selected API base URL. Existing provider and HTTP client concepts can be reused, but iteration 4 requires the live provider to fail closed, classify live failures, preserve write idempotency metadata, and avoid fixture fallback.

## Top-Level Alignment
The live-provider implements the approved architecture’s provider boundary for `live-http` mode. It consumes startup-selected base URL and credential values, executes issue/wiki/comment/create operations, and returns typed results or live-classified errors to sync, write, diagnostics, cache, audit, and integration-test consumers.

## Tasks

### Task 1: HTTPClient enforce live contract
Outcome IDs: outcome-1, outcome-3, outcome-4, outcome-5, outcome-6, outcome-8
Outcome Role: supporting_evidence
Decommission IDs: decommission-1, decommission-2, decommission-3, decommission-4
Change Type: change
Description: The existing live provider already has `Provider`, `ProviderConfig`, `NewLiveProvider`, `HTTPClient`, request models, paged reads, authenticated requests, and write confirmation concepts. This task tightens those entities into the iteration 4 live contract for issue, wiki, comment, and create-issue routes. The component must use only the startup-selected base URL and token it is given, classify live HTTP failures deterministically, and return domain records that downstream cache and audit owners can reconcile.
Existing Behavior / Reuse: Reuse the existing `gitcode.Provider` interface, `liveProvider`, `HTTPClient`, endpoint builders, `Page[T]`, `IssueSummary`, `Issue`, `WikiPage`, `Comment`, `CreateIssueRequest`, `WriteOptions`, `WriteResult[T]`, `ErrAuthExpired`, `ErrForbidden`, `ErrNetworkUnavailable`, `ErrConflict`, and validation error types. Replace any permissive live-provider construction that accepts an empty, relative, malformed, or non-authoritative base URL for explicit live mode with fail-closed validation; keep the offline fixture provider and fixture read-only behavior outside this component. Existing read/write helpers should remain the execution path, but their invariants must prove that live mode cannot synthesize fixture records or retry through fixtures.
Detailed Design: Change `ProviderConfig` validation inside `NewLiveProvider` so `Mode == ProviderModeLive`, `LiveAllowed == true`, non-empty token, and a selected absolute `http` or `https` `BaseURL` are required before constructing `liveProvider`; invalid selected base URLs return a deterministic configuration/provider error before any HTTP request. Keep `HTTPClient` as the live transport owner and ensure its `baseURL.ResolveReference` usage can only target the validated selected base URL, so repository-binding `api_base_url` supplied by CLI startup is the sole authority and environment/default alternatives are not consulted by this component. Reuse `ListIssues`, `ListWikiPages`, `ListIssueComments`, and `CreateIssue` as the component-local API surface, and require each method to validate owner/repo and operation-specific identifiers before request execution, attach `Authorization: Bearer <token>`, bounded timeout/max-response policy, `Accept: application/json`, and `Idempotency-Key` for writes. Preserve paged decoding through `Page[T]`, but require decoded issue/wiki/comment/create payloads to contain the minimal identifiers needed by downstream reconciliation: issue id or number for issue records, wiki slug plus id or version for wiki records, comment id plus parent issue context for comments, and create issue id plus positive number for write confirmations. Keep `writeConfirmedJSON` and `GenerateIdempotencyKey` as the idempotency and confirmation machinery; ensure the create-issue result always sets `Confirmed`, `Operation`, `Target`, `RemoteID`, `RemoteNumber`, `IdempotencyKey`, `ResponseHash`, `ProviderPayloadFingerprint`, and `ConfirmedAt` before returning to write-service. Map HTTP 401 to `ErrAuthExpired` and HTTP 403 to `ErrForbidden` without wrapping them as fixture or generic success; map unreachable selected base URL, timeout, 5xx after retries, oversized payload, malformed JSON, and unsupported payloads to the existing live transport/API validation errors. Enforce the negative invariants for decommissioned behavior by keeping fixture-provider creation out of `NewLiveProvider`, returning no fixture identifiers from live-provider code paths, never returning `fixture client is read-only` from `CreateIssue`, and making 401/403 reachable only after `bytesWithOptions` has sent the live request. The component emits domain results and typed errors only; cache population, audit persistence, missing-credential preflight, doctor JSON, and mock server ownership remain in their respective components.
Acceptance Criteria: Operator runs `gitcode-mcp sync --live` with a valid test token and repository-selected mock base URL; through the real CLI startup product route, live-provider sends authenticated issue/wiki/comment reads to the selected mock server, returns mapped records, downstream cache contains mock records, fixture ids `ISSUE-42` and `WIKI-HOME` are absent, and executable evidence is an offline CLI integration test under `go test ./...`. Operator runs `gitcode-mcp sync --live` with an invalid token against a mock 401/403 endpoint; the live-provider request count is greater than zero, the CLI reports live auth failure, no fixture success appears, and evidence is the corresponding stubbed-external-provider CLI test. Operator runs `gitcode-mcp create-issue --live` with `GITCODE_TOKEN` unset but a mocked credential source resolved by startup; live-provider receives the resolved token, sends an authenticated POST with deterministic idempotency metadata, returns a complete `WriteResult[Issue]`, downstream audit/cache confirmation is visible, `fixture client is read-only` is absent, and evidence is an offline CLI integration test. Operator configures selected and non-selected API endpoints and runs `gitcode-mcp sync --live`; live-provider uses exactly the selected base URL, selected hit count is greater than zero, non-selected hit count is zero or fails unused, and evidence is a mock-server routing test in `go test ./...`.
Workload: 1.8 MM

## Cross-Cutting Constraints
- Explicit live mode is fail-closed — live-provider must not degrade to fixture behavior because CLI startup, sync, write, diagnostics, cache, and integration tests rely on typed live outcomes.
- Secrets remain transport-only — live-provider may consume the token for `Authorization` but must not expose it in errors, write confirmation metadata, fingerprints, or downstream diagnostics.
- Repository binding base URL is authoritative — live-provider must use the selected base URL supplied through configuration and must not choose environment/default alternatives when that value is present.
- Mock API compatibility is minimal and sanitized — live-provider should map only the issue/wiki/comment/create shapes required for acceptance and leave real network e2e outside primary validation.

## Data And Control Flow
- CLI startup constructs `ProviderConfig` — `NewLiveProvider` validates explicit live mode, token presence, and selected base URL before any live operation.
- Sync-service calls live-provider read methods — `HTTPClient` sends authenticated GET requests to the selected base URL, then decoded issue/wiki/comment domain records return to sync-service for cache reconciliation.
- Write-service calls `CreateIssue` — `HTTPClient` sends authenticated POST with idempotency key, then `WriteResult[Issue]` returns with confirmation metadata for audit/cache owners.
- Mock server returns 401/403 — `statusError` maps it to auth/forbidden errors, then diagnostics and sync-service receive live-classified failure after request execution.

## Component Interactions
- `cli-startup` -> `live-provider` — passes explicit live mode, selected repository `api_base_url`, resolved token, timeout, retry, response-size, and pagination policy; live-provider validates and uses these values without alternate discovery.
- `credential-resolution` -> `live-provider` — supplies a resolved token through startup; live-provider treats absence as construction failure and does not perform credential source lookup itself.
- `live-provider` -> `sync-service` — returns `Page[IssueSummary]`, `Page[WikiPage]`, and `Page[Comment]` or typed live errors for cache reconciliation and diagnostics.
- `live-provider` -> `write-service` — returns confirmed `WriteResult[Issue]` with idempotency and remote alias metadata or typed live errors for audit/cache confirmation.
- `live-provider` -> `mock-gitcode-api` — uses GitCode-compatible HTTP endpoints under the selected base URL, enabling request-count evidence without real network access.

## Rationale
The component impact marks live-provider as detailed because it is the component that turns explicit live startup configuration into real authenticated HTTP operations. The existing code already has reusable provider and HTTP-client foundations, so the design is a narrow contract-hardening task rather than a rewrite.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0235-run_attempt-1/final_message.txt`
