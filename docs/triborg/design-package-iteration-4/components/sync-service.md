# Design Package Component: sync-service

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Sync Service

## Summary
The sync-service is materially affected because it owns sync command semantics and cache reconciliation after CLI startup selects either live or fixture provider mode. It must preserve existing offline fixture sync while enforcing live-mode invariants for provider identity, comment parent linkage, fixture-leak rejection, and auth-failure normalization.

## Top-Level Alignment
The component implements the service-side portion of live sync readiness: it consumes the selected provider, stages issue/wiki/comment data, reconciles successful live records into cache state, and keeps default non-live sync fixture-backed. It does not own credential resolution, base URL selection, mock server setup, or CLI startup composition.

## Tasks

### Task 1: SyncGraph live reconciliation
Outcome IDs: outcome-1, outcome-4, outcome-5, outcome-7
Outcome Role: supporting_evidence
Decommission IDs: decommission-1, decommission-2, decommission-3
Change Type: change
Description: Change the sync-service execution path so `Service` reconciles live issue, wiki, and comment records from the selected provider while preserving the existing fixture provider path for default sync. The local sync contract must distinguish live provider failures from fixture behavior and reject deterministic fixture/provenance leakage in live mode. This task covers `SyncToCache`, `SyncResources`, `BulkSyncIssues`, `BulkSyncWiki`, `BulkSyncAll`, `fetchAndStage`, `stageIssue`, `stageWiki`, the existing comment staging/cache graph path, and `normalizeSyncFailure`.
Existing Behavior / Reuse: Reuse the existing `Service` provider-mode field, configured `gitcode.Client`, repository-scope validation, writer lease, idempotency handling, `SourceGraph`/`SyncGraph` cache reconciliation, comment staging, chunk generation, and partial-sync result behavior. Existing source inspection confirms `NewWithMode` can construct fixture or live clients and `SyncToCache` already stages issue/wiki records, but live-mode provider ID aliasing, comment parent validation, fixture provenance rejection, and `live_auth_failure` classification are not complete in the sync-service boundary. Keep `sanitizedFixtureClient` and fallback IDs for `ProviderModeFixture`; replace only live-mode fallback-to-fixture acceptance by explicit live invariants.
Detailed Design: Add or narrow component-local helpers around the minimum invariants: `syncProviderMode()` for reading the selected mode, source/alias resolution for issue/wiki/comment records, live graph validation, comment parent reconciliation, and `normalizeSyncFailure` classification. `stageIssue` and `stageWiki` must preserve explicit `StableID` and existing stable cache identity semantics first; when live mode is active, provider payload IDs such as `gitcode.Issue.ID` and `gitcode.WikiPage.ID` are required and stored as remote aliases or identity-map metadata when they are not the stable source ID, so live provider IDs support reconciliation and fixture-leak tests without replacing an existing stable source identity. Fixture mode keeps the current `ISSUE-<remoteID>` and `WIKI-<remoteID>` fallback behavior.

Extend the existing comment staging/cache graph path used by issue sync so live `gitcode.Comment` payloads are staged into the same issue `SyncGraph` or equivalent discussion state. For each live comment, require the provider comment ID, preserve it as the comment stable identity only when no stronger existing stable identity applies, otherwise store it as a remote alias/identity-map entry, preserve the provider parent issue ID or alias metadata, reconcile that parent to the staged issue source identity or alias set, and commit comments with the issue graph through the existing cache handoff. Invalid comment parent linkage fails the affected issue resource before cache mutation.

Before `UpsertSyncGraph` or equivalent cache commit, live mode must validate deterministic anchors only: forbidden fixture IDs such as `ISSUE-42` and `WIKI-HOME`, empty required provider IDs for live issues/wiki/comments, fixture provider mode or fixture provenance appearing in a live graph, and unreconciled comment parent issue linkage. Do not reject comment content by shape, substring, or “fixture-like” text heuristics. Fixture mode skips these live-only guards and continues through existing fixture staging behavior.

`normalizeSyncFailure` must map live-provider 401/403 errors into an `ErrSyncFailure` mode/value recognizable as `live_auth_failure`, while preserving existing retry, rate-limit, payload-size, collision, not-found, conflict, and partial failure behavior. `BulkSyncIssues`, `BulkSyncWiki`, and `BulkSyncAll` must continue to list through the already-selected client, turn summaries into per-resource `SyncRequest` values, and call `SyncResources`; the invariant is that live mode cannot convert provider errors into fixture success, while default sync through `ProviderModeFixture` keeps producing existing fixture-backed cache results and zero live HTTP traffic. For decommissioning, fixture fallback results and fixture IDs remain valid only inside fixture-mode runtime/tests; product live-mode cache writes are blocked by the graph validator, and live auth failures return before any successful cache graph is committed.
Acceptance Criteria: Operator runs `gitcode-mcp sync --live` through the real CLI route with a valid test token against a stubbed external GitCode HTTP server; the sync-service route fetches issues, wiki pages, and comments through the live client, stages `MOCK-COMMENT-1` through the named comment staging/cache graph path, attaches it to the staged `MOCK-ISSUE-100` issue graph by reconciled parent identity or alias, commits it to cache discussion/comment state with `MOCK-WIKI-LIVE`, and `go test ./...` includes an offline CLI integration test proving `ISSUE-42` and `WIKI-HOME` are absent. Operator runs `gitcode-mcp sync --live` with an invalid test token against a stubbed server returning 401 or 403; the product surface reports `live_auth_failure`, no successful fixture sync result is recorded, and the executable CLI integration test proves the mock server request count is greater than zero. Operator runs `gitcode-mcp sync --live` with a live comment missing a provider ID, carrying forbidden fixture IDs, carrying fixture provider/provenance in a live graph, or referencing an unreconciled parent issue; the sync-service rejects the staged graph before cache commit, returns a structured sync failure, and a stubbed-external-provider integration test proves no invalid comment state is visible in the cache runtime. Operator runs `gitcode-mcp sync` without `--live` while the mock server is available; the existing fixture-backed sync behavior completes, the mock server request count remains zero, and the local Go test suite proves default sync behavior is unchanged. System runs bulk live sync for issues and wiki together; partial failures remain structured through `SyncResources`, successful live resources are committed atomically per resource, and failed live resources never write fixture-derived cache graphs.
Workload: 1.5 MM

## Cross-Cutting Constraints
- Live mode is fail-closed and must not degrade to fixture-backed sync results — this applies across CLI startup, provider, diagnostics, and cache validation boundaries
- Default non-live sync remains fixture-backed and network-free — this protects existing cache-first behavior while live readiness is added narrowly
- Stable source IDs are preserved while provider IDs remain available as aliases or identity metadata — this keeps cache identity compatible with the approved architecture
- Sanitized mock/live records must be reconciled through runtime cache surfaces, not source-level claims — sync-service owns the cache graph handed to cache-runtime
- Explicit network commands should fail visibly on auth/config errors rather than silently using offline fixtures — this supports operator clarity for `--live` sync
- Live comment data must preserve provider identity and parent linkage before cache mutation — issue discussion/comment state is part of the Task 5 live sync acceptance signal

## Data And Control Flow
- CLI startup constructs `Service` with effective provider mode and selected provider client — sync-service consumes this mode and never reselects credentials or base URL
- `BulkSyncIssues` and `BulkSyncWiki` list remote summaries through the selected provider client — each summary becomes a scoped `SyncRequest` passed to `SyncResources`
- `SyncToCache` resolves repository scope, acquires the writer lease, stages provider issue/wiki/comment records, validates live-mode invariants, and commits a `SyncGraph` — cache mutation occurs only after provider data passes sync-service checks
- Live issue sync stages comments through the existing comment staging/cache graph path — each provider comment ID is preserved as stable identity or alias metadata, each parent issue ID is reconciled to the staged issue, and the resulting issue discussion/comment state is committed with the issue graph
- Provider 401/403 errors flow into `normalizeSyncFailure` before returning to CLI diagnostics — live auth failure remains visible and does not create fixture success state
- Non-live `ProviderModeFixture` flows through the existing fixture client and fallback source IDs — no live HTTP call is introduced by sync-service

## Component Interactions
- `cli-startup` -> `sync-service` — passes a `Service` instance with effective provider mode and selected provider client; sync-service consumes but does not own startup composition
- `live-provider` -> `sync-service` — supplies `gitcode.Issue`, `gitcode.WikiPage`, and `gitcode.Comment` payloads with sanitized IDs and parent issue IDs required for live cache reconciliation
- `fixture-provider` -> `sync-service` — supplies existing fixture issue/wiki/comment payloads only when provider mode is fixture
- `sync-service` -> `cache-runtime` — commits `SyncGraph` records, comments, identities, chunks, remote revisions, and sync events only after live-mode guard checks pass
- `sync-service` -> `diagnostics` — returns typed sync failures such as `live_auth_failure` for CLI formatting and test assertions
- `mock-gitcode-api` -> `sync-service` — acts only as an external-provider substitute reached through the live provider client during CLI integration tests

## Rationale
The sync-service owns the local semantics that turn provider reads into durable cache state, so it must enforce the live/offline distinction after CLI startup selects the provider. A single component-local change is sufficient because the delta concerns one sync command service contract: provider invocation, issue/wiki/comment staging, parent-linked comment reconciliation, error normalization, partial sync behavior, and deterministic fixture-leak rejection.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0552-run_attempt-1/final_message.txt`
