# Design Package Component: audit-runtime

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Audit Runtime

## Summary
Audit runtime is affected because live `create-issue --live` must persist an inspectable, public-safe confirmation after the live provider confirms a write. Iteration 4 requires this confirmation to support offline validation of live write behavior without storing secrets or raw API material.

## Top-Level Alignment
The approved architecture assigns `audit-runtime` ownership of non-secret write confirmations for live create issue. This component supplies supporting evidence consumed by write-service and CLI integration tests for Task 6 and Task 10.

## Tasks

### Task 1: Persist audit confirmations
Outcome IDs: outcome-6, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-4
Change Type: change
Description: The audit runtime must persist live create-issue confirmations that can be inspected after the real CLI path completes. Existing idempotency audit entries already record operation, status, remote identity, payload hash, and timestamps, but they do not clearly model the iteration-4 `AuditConfirmationRecord` contract with live mode and sanitized request metadata. This task changes the audit confirmation entity and persistence path so live write success has enough non-secret evidence for runtime acceptance.
Existing Behavior / Reuse: Reuse the existing audit package idempotency lookup and success/failure entry constructors, the cache store audit persistence API, and the existing audit trail table semantics. Confirmed absent: there is no dedicated public-safe live write confirmation shape that records live mode and sanitized request metadata while enforcing absence of token, Authorization header, cookie, private URL, or raw API body. Existing fixture read-only behavior is not an audit-runtime concern, but audit runtime must only persist confirmations produced after a live provider confirmation, not fixture write failures.
Detailed Design: Extend the component-local audit confirmation model around the existing `AuditTrailEntry` concept so a successful live create issue can carry `command`, `mode`, `remote_alias` or remote id, `idempotency_reference`, `created_at`, `status`, `payload_hash`, and sanitized request metadata. Add or change audit constructors in the audit runtime so the write-service can create a live success confirmation only after the provider returns a confirmed remote result; the constructor must normalize command names, trim idempotency keys, preserve deterministic payload hash comparison, and reject or omit secret-bearing metadata keys such as token, authorization, cookie, raw request body, and unredacted endpoint URL. Preserve the existing idempotency invariant: the same repo and idempotency key with the same payload hash replays the existing success, while a different payload hash remains a conflict and does not create a second success confirmation. For `decommission-4`, the fixture read-only route is replaced as a source of live write evidence by requiring live mode plus confirmed remote identity before any success audit record can be emitted; missing remote identity or unconfirmed provider result is stored as failure/partial state rather than a success confirmation.
Acceptance Criteria: When an operator runs `gitcode-mcp create-issue --live` with a valid test token against the mocked GitCode API, the write route records an audit confirmation visible through the runtime audit lookup for the repo and idempotency key; the record includes command metadata, live mode, idempotency reference, timestamp, status, payload hash, and remote alias or remote id. The stored audit data contains no token, Authorization header, cookie, private URL, or raw API body, and the command output does not contain `fixture client is read-only`. Executable evidence is an offline Go integration or service/runtime test using a stubbed external GitCode HTTP provider plus the real cache/audit store, and `go test ./...` must pass.
Workload: 0.8 MM

## Cross-Cutting Constraints
- Public-safe audit persistence — audit records are durable and inspectable, so they must not store credentials, cookies, raw API bodies, private URLs, or Authorization header material.
- Idempotent write evidence — audit confirmations must preserve deterministic replay/conflict behavior for repeated live create commands.
- Offline validation compatibility — the persisted confirmation must be inspectable by local tests without real credentials, external network, or OS Keychain access.

## Data And Control Flow
- Live create issue succeeds — write-service receives confirmed provider result — audit-runtime persists the sanitized success confirmation before cache confirmation is finalized.
- Duplicate idempotency key arrives — write-service asks audit-runtime for the prior entry — audit-runtime returns replay, retry, partial, or conflict using repo id, key, status, and payload hash ordering.
- Audit write fails after remote confirmation — audit-runtime records or exposes partial failure state — write-service reports partial remote-confirmed audit failure without pretending the write was not sent.

## Component Interactions
- `write-service` -> `audit-runtime` — passes live write confirmation inputs after provider confirmation; audit-runtime owns success/failure/partial audit record construction and idempotency lookup.
- `audit-runtime` -> `cache-runtime` — persists audit entries in the existing store surface so tests and runtime status can inspect write confirmations by repo and idempotency key.
- `cli-integration-tests` -> `audit-runtime` — verifies live create issue records sanitized confirmation state after the real CLI startup path runs.

## Rationale
The component is detailed because Component Impact identifies a required audit-runtime delta for live write confirmations. Existing audit entries provide reusable idempotency and persistence primitives, but the approved architecture requires explicit public-safe confirmation semantics for live create issue acceptance.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0255-run_attempt-1/final_message.txt`
