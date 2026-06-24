# Design Package Component: mock-gitcode-api

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Mock GitCode API

## Summary
The `mock-gitcode-api` component is affected because iteration 4 requires offline proof that explicit live CLI routes use GitCode-compatible HTTP behavior without real network, real credentials, or OS Keychain. The component-local work is a sanitized `httptest` harness with request counters, configurable auth outcomes, live payloads, create responses, and selected versus non-selected base URL evidence.

## Top-Level Alignment
`mock-gitcode-api` implements the architecture validation boundary for live provider wiring, credential failure timing, auth failure classification, live sync payload reconciliation, live create issue write evidence, offline default isolation, and API base URL authority. It is supporting evidence only; product behavior remains owned by CLI startup, providers, services, cache, audit, diagnostics, and repository binding.

## Tasks

### Task 1: MockGitCodeAPI harness validate
Outcome IDs: outcome-1, outcome-2, outcome-3, outcome-4, outcome-5, outcome-6, outcome-7, outcome-8, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-1, decommission-2, decommission-3, decommission-4
Change Type: validate
Description: Create a component-local sanitized GitCode-compatible `httptest.Server` harness that offline CLI integration tests can use as the live API endpoint. The harness owns mock issue, wiki, comment, create issue, auth-failure, and request-counter behavior while the real CLI startup path remains the target under test. It also provides selected and non-selected server instances so tests can prove repository binding `api_base_url` authority by hit counts.
Existing Behavior / Reuse: Existing tests already use ad hoc `httptest.Server` handlers for isolated service and MCP scenarios, and fixture identifiers such as `ISSUE-42` and `WIKI-HOME` exist for offline fixture behavior. No reusable component-local GitCode-compatible live-readiness harness currently exists that combines sanitized payloads, configurable auth behavior, create issue handling, endpoint counters, and selected/non-selected routing evidence for real CLI startup tests. Reuse the existing Go `httptest` pattern and target runtime cache/audit inspection concepts, but replace scattered one-off live handlers for iteration-4 acceptance with this named harness.
Detailed Design: Add a test-only `MockGitCodeAPI` harness entity with a constructor that starts an `httptest.Server`, exposes `BaseURL`, and records per-operation counters for list issues, list wiki pages, list comments, create issue, auth failures, unexpected requests, and total requests. Add configuration fields for expected bearer token, auth mode (`accept`, `reject401`, `reject403`), repository owner/name, sanitized issue/wiki/comment payloads, create issue response payload, and whether create conflicts should return a deterministic 409 response. The request dispatcher must validate method, path, repository scope, `Authorization`, and for create issue the idempotency header before returning JSON; auth rejection must increment counters and return 401 or 403 after a request reaches the server.

The harness must expose immutable snapshot methods such as `Counts()` and captured request accessors that return non-secret metadata only: method, path, operation, idempotency key presence/value, and whether authorization matched, never the raw token. Payload builders must use stable sanitized live identifiers such as `MOCK-ISSUE-100`, `MOCK-WIKI-LIVE`, and `MOCK-COMMENT-1`, and must not emit fixture-only identifiers `ISSUE-42` or `WIKI-HOME` in live-mode responses. The issue list, wiki list, comment list, and create issue response shapes should match the existing live provider adapter expectations closely enough for the real CLI sync and create routes to parse them without component-specific test shims.

Add a selected/non-selected routing helper that can start two `MockGitCodeAPI` instances: one authoritative server configured as the repository binding `api_base_url`, and one non-authoritative server intended to remain untouched or fail if called. Tests using this helper must assert selected counters are greater than zero for live sync and non-selected counters remain zero, proving the mock component supports Task 8 without owning repository-binding logic. For missing-credential and offline default scenarios, the same harness must be available before the CLI command runs, then report zero requests after the command, proving no HTTP attempt occurred.

For decommission coverage, the harness must enforce negative invariants in executable assertions: live sync evidence fails if live cache/output contains `ISSUE-42` or `WIKI-HOME`; invalid-token evidence fails unless the server saw at least one request and returned 401 or 403; live sync cache evidence fails if only fixture records appear; create issue evidence fails if output or error contains `fixture client is read-only` or if no POST create request was captured. Existing fixture-backed behavior is explicitly kept for non-live tests, but the harness keeps those fixture identifiers out of live mock payloads and uses counters to prove no silent live-to-fixture fallback satisfied live acceptance.
Acceptance Criteria: Developer runs the offline test suite through `go test ./...`; the target product surface is the real CLI sync/create/doctor startup path exercised by CLI integration tests with `MockGitCodeAPI` as the external GitCode dependency. With a valid mocked credential, `gitcode-mcp sync --live` causes issue/wiki/comment counters to increment, returns parseable sanitized JSON, and runtime cache assertions find mock records while rejecting `ISSUE-42` and `WIKI-HOME`. With no credential, `gitcode-mcp sync --live` returns typed missing-credential diagnostics and `MockGitCodeAPI.Counts().TotalRequests` remains zero; with an invalid token, the server records at least one request and the CLI reports live auth failure. With `gitcode-mcp create-issue --live`, the harness captures an authenticated POST plus idempotency metadata and runtime audit/cache confirmation is verified; with plain `gitcode-mcp sync`, all mock counters remain zero; with selected/non-selected base URLs, only the selected server records traffic.
Workload: 1.5 MM

## Cross-Cutting Constraints
- Sanitized mock payloads only — the harness must never require or store real credentials, cookies, internal URLs, or unsanitized API captures because the repository is public-safe and tests must run offline.
- External-dependency mock boundary — `mock-gitcode-api` may replace GitCode HTTP only, while CLI startup, cache, audit, provider construction, and command services remain real target runtime paths.
- Non-secret observability — counters and captured request metadata may expose operation, path, status, and idempotency evidence, but never token secret values.
- Fixture-leak rejection — live-mode mock payloads and assertions must reject `ISSUE-42`, `WIKI-HOME`, and `fixture client is read-only` to prove decommissioned fallback behavior stays replaced.

## Data And Control Flow
- CLI integration test starts `MockGitCodeAPI` — test harness and mock server — server exposes `BaseURL` before repository binding is configured.
- Real CLI command runs with temporary config — CLI startup, credential resolution, repository binding, live provider — live provider sends HTTP only when admitted by live configuration and credentials.
- `MockGitCodeAPI` dispatches GitCode-compatible routes — live provider and mock server — handlers classify request as issue, wiki, comment, create, auth failure, or unexpected request and update counters before response.
- Runtime assertions inspect cache/audit and counters — CLI integration tests, cache runtime, audit runtime, mock server — evidence compares mock records and request counts against expected live/offline scenarios.
- Selected/non-selected authority check runs two mock servers — repository binding, live provider, mock server — only the authoritative `api_base_url` server may receive requests when repository binding provides it.

## Component Interactions
- `mock-gitcode-api` -> `cli-integration-tests` — exposes `BaseURL`, configured auth mode, sanitized payloads, captured create request metadata, and request counter snapshots for offline acceptance assertions.
- `live-provider` -> `mock-gitcode-api` — sends authenticated GitCode-compatible HTTP list and create requests to the selected server using the same adapter paths that production live mode uses.
- `repository-binding` -> `mock-gitcode-api` — consumes mock `BaseURL` as repository binding `api_base_url` in tests, making the mock server the authoritative endpoint for explicit live commands.
- `credential-resolution` -> `mock-gitcode-api` — supplies accepted or invalid bearer-token behavior indirectly through CLI startup; the mock server verifies whether the request used the expected authorization without exposing the token.
- `cache-runtime` -> `mock-gitcode-api` — cache assertions compare stored records against sanitized mock issue/wiki/comment/create payload identifiers produced by the harness.
- `audit-runtime` -> `mock-gitcode-api` — create issue audit assertions compare non-secret idempotency and remote alias evidence against the captured mock POST response and metadata.

## Rationale
The approved architecture makes `mock-gitcode-api` the offline compatibility boundary that proves live-readiness without external GitCode access. The inspected repository already has isolated `httptest` usage and fixture records, but not a unified component-local harness that validates all iteration-4 live CLI scenarios through real startup, counters, sanitized payloads, auth modes, and base URL routing evidence.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0133-run_attempt-1/final_message.txt`
