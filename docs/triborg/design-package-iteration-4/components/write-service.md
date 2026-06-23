# Design Package Component: write-service

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Write Service

## Summary
The `write-service` component is affected because `create-issue --live` must consume the shared credential-resolution result, execute through the live provider write route, preserve idempotency, and record audit/cache confirmation. The component impact contains one detailed delta, so this design keeps the scope to the live create-issue write flow and fixture-read-only decommissioning.

## Top-Level Alignment
`write-service` implements the approved live create issue use case by coordinating credential admission, provider `CreateIssue` invocation, idempotency replay/conflict handling, audit recording, and cache confirmation. For this configured component, it resolves Request Tasks 3 and 6 only.

## Tasks

### Task 1: Live CreateIssue contract
Outcome IDs: outcome-3, outcome-6
Outcome Role: supporting_evidence
Decommission IDs: decommission-4
Change Type: change
Description: The write-service owns `create-issue --live` command semantics after CLI startup has selected live mode and supplied a live provider. Its local role is to accept the shared resolved credential state, require that state before live writes, call the provider `CreateIssue` operation with a stable idempotency key, and convert the provider confirmation into audit and cache state. It also classifies accidental fixture read-only routing as fixture fallback instead of surfacing it as an ordinary live provider error.
Existing Behavior / Reuse: Reuse the existing `Service.CreateIssue`, `executeWrite`, `WriteCommandRequest`, `WriteCommandResult`, `writeIdempotency`, `audit.LookupIdempotency`, `callWriteAdapter`, `issueWriteGraph`, `audit.Success`, `audit.Failure`, `store.RecordAuditEvent`, and `store.UpsertRecordGraph` concepts. Existing behavior already validates title, requires `WriteModeLive` or `WriteModeDryRun`, computes idempotency fingerprints, replays matching audit entries, detects idempotency conflicts, calls the GitCode client, records audit events, and refreshes cache records. Confirm absent or replace only the remaining live-readiness gap: live create must not use a divergent process-local environment fallback when CLI startup has already resolved credentials, and `fixture client is read-only` must not escape in live mode.
Detailed Design: Change the write-service live credential gate so `executeWrite` treats the service’s resolved live credential state as the authoritative input for `WriteModeLive`. Populate the existing `writeCredentialPresent` field or equivalent service configuration from CLI startup’s shared credential-resolution result; make `hasWriteCredential` a pure check of live provider mode plus that resolved state for live writes, while preserving dry-run behavior without credentials. Keep environment-token discovery outside write-service so auth status, doctor, sync, live provider construction, and write gates cannot diverge; if a compatibility fallback remains for older constructors, constrain it to explicit legacy construction and never allow it to override a resolved missing-credential result from live startup.

For `create-issue`, keep routing through `Service.CreateIssue` into `executeWrite("create-issue", ..., RepositoryScopeIssues)` and then `callWriteAdapter`. `callWriteAdapter` must pass normalized owner, repository name, trimmed title, body, labels, and `gitcode.WriteOptions{IdempotencyKey: key}` to the live client’s `CreateIssue` method. The invariant is that the remote create call is attempted exactly once for a new non-replayed idempotency key after credential admission, and zero times for missing credentials, idempotent replay, or idempotency conflict.

Keep `writeIdempotency` as the deterministic key and fingerprint authority. Before provider invocation, `audit.LookupIdempotency` must produce one of four outcomes: replay matching success as `already_applied`, reject fingerprint mismatch as `write_idempotency_conflict`, repair partial cache-confirmed state through `replayWriteGraph`, or allow a new provider write. Duplicate create requests with the same idempotency key and same title/body/labels return deterministic replay, while the same key with different payload is rejected before a second remote write.

Keep `issueWriteGraph` as the cache-confirmation builder for successful create issue results. It must require a confirmed provider result with a non-empty remote id derived from `RemoteID`, issue id, remote number, or issue number; otherwise `executeWrite` records a failure audit event and returns `write_unconfirmed_remote`. On success, write-service records a non-secret audit success containing repo id, command, idempotency key, record id, remote type, remote id, fingerprint, message, and timestamp, then upserts the issue record graph into cache; partial audit/cache failures keep explicit partial failure codes.

For `decommission-4`, do not delete the offline `sanitizedFixtureClient`, because fixture-provider still owns non-live behavior. Disable fixture read-only behavior in product live runtime by enforcing `ProviderModeLive` plus resolved credential state for live writes and by adding a live-mode guard around provider errors: if `callWriteAdapter` returns an invalid-query/read-only error matching the fixture client contract while `req.Mode == WriteModeLive`, convert it to `fixture_fallback_detected` or `write_fixture_fallback_detected`. The negative invariant is that `create-issue --live` output and error state never contain `fixture client is read-only`.
Acceptance Criteria: Operator runs `gitcode-mcp create-issue --live --repo <repo> --title <title> --idempotency-key <key>` with `GITCODE_TOKEN` unset and a mocked Keychain-equivalent credential already resolved by CLI startup; the target product surface is the write-service route behind the CLI command; the mock GitCode API receives one authenticated create request with the expected idempotency key, and the CLI reports live create success with remote id plus audit/cache confirmation; executable evidence is an offline CLI integration test using a stubbed external HTTP provider under `go test ./...`. Operator repeats the same command with the same idempotency key and same payload; the write-service route returns deterministic replay or already-applied state without a second create side effect, and audit/cache state remains inspectable through runtime APIs; executable evidence is a local Go test that asserts request counts, audit entry, cache record, and replay result. Operator repeats the same idempotency key with a different payload; the route rejects with `write_idempotency_conflict` before another provider write; executable evidence is a service or CLI integration test with mock request count unchanged after the conflict. Operator runs `create-issue --live` when the service is accidentally backed by the fixture read-only client; the route returns the fixture-fallback diagnostic code and never exposes `fixture client is read-only`; executable evidence is a write-service test or CLI integration test that exercises the live product path and asserts the forbidden string is absent.
Workload: 0.6 MM

## Cross-Cutting Constraints
- Shared credential resolution remains outside write-service — write-service consumes resolved credential presence so auth status, doctor, sync, provider construction, and writes use one credential pipeline.
- Live writes are fail-closed — `WriteModeLive` rejects missing credentials and fixture fallback instead of degrading to offline fixture behavior.
- Audit and cache confirmations contain no secrets — write-service records idempotency, repo, command, remote aliases, fingerprints, and timestamps, never token material.
- Idempotency is deterministic — the same key and payload replay consistently, while the same key and different payload conflicts before a second remote write.

## Data And Control Flow
- CLI startup resolves live mode and credential state — `write-service` receives a live-configured service/provider — write-service does not rediscover credentials independently.
- `CreateIssue` validates command input — `executeWrite` computes idempotency and checks credential admission — provider invocation is allowed only after audit replay/conflict checks.
- Live provider returns create confirmation — `issueWriteGraph` maps it to cache state — `audit.Success` and cache upsert complete the write confirmation before success is returned.
- Fixture read-only error appears in live write route — write-service classifies it as fixture fallback — user-visible output excludes the fixture read-only string.

## Component Interactions
- `credential-resolution` -> `write-service` — provides resolved credential presence/source metadata through CLI startup or service configuration; write-service consumes only presence for gating and never reads secret values directly.
- `write-service` -> `live-provider` — calls `CreateIssue` with normalized repo route, issue payload, and idempotency key after credential and audit admission.
- `write-service` -> `audit-runtime` — records non-secret success, failure, replay, partial-audit, and partial-cache states keyed by repo id and idempotency key.
- `write-service` -> `cache-runtime` — upserts the confirmed issue record graph and remote alias/version state after provider confirmation.
- `cli-integration-tests` -> `write-service` — exercises the real CLI create-issue path with mocked external GitCode HTTP behavior and verifies request count, audit/cache state, replay behavior, conflict behavior, and forbidden fixture output absence.

## Rationale
The component impact delta is detailed because live create issue is directly owned by write-service. Existing write-service anchors already cover most of the local flow, so the task focuses on consuming shared startup credential state, preserving idempotency/audit/cache machinery, and enforcing the decommissioned fixture-read-only invariant for live mode.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0247-run_attempt-1/final_message.txt`
