# Design Package Component: sync-engine

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Sync Engine

## Summary
The sync-engine component is detailed because Component Impact marks `sync-engine` as requiring service-layer implementation of cache freshness, explicit sync reconciliation, lock contention behavior, idempotency semantics, and sync failure handling. The current repository only has the CLI scaffold, so the required sync-engine behavior does not already exist.

## Top-Level Alignment
The sync engine lives inside the approved `internal/service` orchestration boundary. It coordinates `internal/cache` and `internal/gitcode` interfaces for explicit sync paths while preserving the architecture invariant that routine reads remain cache-first and offline.

## Tasks

### Task 1: Add SyncToCache state machine
Outcome IDs: outcome-6, outcome-13
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Add the service-layer sync engine that computes record freshness and executes explicit cache refresh. This task owns component-local orchestration for idempotency replay, lock acquisition, sync-event state transitions, bounded staging, transactional reconciliation, and lock release. It depends on `internal/cache.Store` for database mutation and `internal/gitcode.Client` for already-paginated, typed remote results.
Existing Behavior / Reuse: The current source tree has only `cmd/gitcode-mcp` and an `internal/cli` scaffold whose known commands return "not implemented yet"; no `internal/service` package, `SyncToCache`, `GetSyncStatus`, lock orchestration, idempotency replay, or sync reconciliation behavior exists. Reuse the approved architecture's `cache.Store` and `gitcode.Client` concepts as dependency interfaces rather than duplicating storage or HTTP logic. Reuse adapter-owned pagination, `Retry-After` parsing, low-level transport retry/timeout classification, and typed errors instead of implementing page-fetch policy in the service layer.
Detailed Design: Add a service-layer `Service` entity with `GetSyncStatus(ctx, id)` and `SyncToCache(ctx, request)` methods. Add component-local data structures: `SyncRequest{Source, TrackerID, StableID, RemoteAlias, IdempotencyKey, MaxAttempts, BackoffBase, BackoffMax, Timeout, MaxSize}` and `SyncResult{IdempotencyKey, Status, Counts, Replayed, SyncEventID, Freshness}`. If `SyncRequest.IdempotencyKey` is empty, `SyncToCache` generates one from normalized sync source, target, args, and request timestamp; if present, the caller-supplied key is used unchanged.

`GetSyncStatus` resolves the stable id or remote alias through the cache interface, reads source version and `remote_revisions` metadata, and returns stable id, remote alias, local updated time, last fetched time, remote version time/hash when available, and freshness state `fresh`, `stale`, `missing_remote`, or `unknown`. `GetSyncStatus` must not call the GitCode adapter.

`SyncToCache` starts with an idempotency lookup before lock acquisition. If the key has a prior `succeeded` event, it returns the stored successful `SyncResult` with `Replayed=true` without acquiring the sync lock, calling the remote, or mutating cache state. If the key is `in_progress`, it returns a distinct typed `ErrSyncInProgress` result containing the active event id and key; if the key is absent or retryable after a prior `failed` event, it proceeds to lock acquisition. This ordering ensures successful replay works even while another unrelated sync lock is held.

After idempotency replay checks, `SyncToCache` executes: `AcquireLock -> begin/mark sync event in_progress -> cache integrity preflight -> adapter fetch -> bounded staging -> transactional reconcile -> finalize sync event -> ReleaseLock`. Lock release is deferred so cancellation and errors do not leave a held process lock. Event states are `in_progress`, `succeeded`, `failed`, and `replayed`; transition to `succeeded` happens only after all reconciliation writes commit, and transition to `failed` records typed error evidence without committing source/version/conflict mutations.

The GitCode adapter handoff is explicit: `internal/gitcode.Client` owns HTTP pagination, page/per_page or cursor strategy, `Retry-After` parsing, transport-level retry/timeout classification, response-size classification where available, and conversion to typed errors such as `ErrNetworkUnavailable`, `ErrRateLimited`, `ErrAuthExpired`, `ErrPartialResponse`, `ErrRemoteNotFound`, and `ErrPayloadTooLarge`. `SyncToCache` only decides sync-level re-attempts around complete adapter calls when a typed error is retryable and the idempotency key still permits retry. Retryable service-level re-attempt inputs are typed `ErrNetworkUnavailable`, `ErrRateLimited`, and transient adapter server errors if represented by the adapter error family; non-retryable inputs are `ErrAuthExpired`, `ErrPartialResponse`, `ErrRemoteCollision`, `ErrCacheCorruption`, `ErrRemoteNotFound` for a known alias, `ErrPayloadTooLarge`, validation errors, and context cancellation. For `ErrRateLimited`, the adapter-provided `RetryAfter` field controls service-level delay when retrying is possible; service must not sleep beyond the request/context deadline.

Remote data is staged before reconciliation using a bounded normalized representation, not partial source rows. The staging invariant is: no `sources`, `identity_map`, `remote_revisions`, or `conflicts` rows are mutated until the complete requested sync scope has been fetched and normalized under `MaxSize`, configured page scope, and context deadline. The staged set contains only normalized remote records, remote aliases, remote version metadata, and computed content hashes; if staging exceeds `MaxSize` or scope limits, `SyncToCache` fails the sync event and exposes no partial records. If memory staging is not sufficient, the cache interface may provide transaction-local staging, but staged rows must remain invisible to normal cache readers and be discarded on rollback.

Reconciliation runs inside one cache transaction. For each staged record, compute a deterministic content hash from normalized remote issue/wiki fields, look up existing records by remote alias through the cache identity map, skip unchanged records, upsert changed records when no local-only edits are present, create conflict records when local-only edits would be overwritten, insert new source and identity-map rows for unknown aliases, update remote revisions, and record sync-event counts and evidence. On typed recoverable failures after service-level attempts are exhausted, the method returns the typed error and leaves source, identity, version, and conflict rows unchanged except for failed sync-event audit evidence; on lock contention it returns `ErrLockContention` before remote fetch or mutation.
Acceptance Criteria: A developer triggers the service API by running `go test ./internal/service/... -run TestSync`; the test inserts a stale cache record, calls `GetSyncStatus`, and receives a stale result for the target stable id. The same test runs `SyncToCache` against fixture-backed remote data, then observes updated source content, a fresh `GetSyncStatus` result, and a `sync_events` row containing the idempotency key, `succeeded` state, and reconciliation counts. A concurrent-access test holds the cache lock and calls `SyncToCache` again through the same service surface; the second call returns typed `ErrLockContention`, performs no remote fetch, and leaves cache records unchanged. A retry test runs `SyncToCache` against a fixture-backed adapter that returns retryable typed `ErrRateLimited` with `RetryAfter` and/or typed temporary timeout before success; the service re-attempts only at the sync-call level, then commits exactly once after a complete successful adapter result. An idempotency replay test calls `SyncToCache` twice with the same idempotency key after a successful first run while an unrelated lock is held; the second call returns the prior result with `Replayed=true`, does not acquire the lock, creates no duplicate source/version/conflict mutations, and does not call the remote fixture server. A bounded-staging test forces an error after partial remote data is received or after `MaxSize` is exceeded and verifies no partial source/version/conflict rows are visible.
Workload: 2.4 MM

### Task 2: Add sync failure guards
Outcome IDs: outcome-13
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Add sync-engine handling and executable coverage for the nine failure modes defined by the architecture. The component-local role is to translate adapter/cache failures into stable typed service results while enforcing cache-state guarantees. This task complements the state machine by proving each failure path preserves the required mutation boundaries.
Existing Behavior / Reuse: No service-layer failure-mode handling exists today, and the CLI scaffold does not call adapter or cache code. Reuse the typed error concepts from the approved architecture: `ErrNetworkUnavailable`, `ErrRateLimited`, `ErrAuthExpired`, `ErrPartialResponse`, `ErrRemoteCollision`, `ErrCacheCorruption`, `ErrLockContention`, `ErrRemoteNotFound`, and `ErrPayloadTooLarge`. Reuse adapter tests for HTTP-specific behavior; service tests exercise only sync-engine orchestration through `SyncToCache`.
Detailed Design: Add sync-engine error handling branches around adapter typed results, bounded staging, alias reconciliation, lock acquisition, sync-event state transitions, and cache integrity preflight. The service must preserve these invariants: timeout after retry exhaustion, partial response, rate limit after retry exhaustion, auth expiry, and oversized payload do not write partial source/version rows; remote id collision blocks alias mutation and returns collision evidence through the cache contract; cache corruption prevents further writes; known-alias 404 keeps the local source and marks remote version as not found only through the cache interface; lock contention exits before network access.

The failure guards use the same transaction boundary as `SyncToCache`: failed sync-event evidence may be recorded, but source, identity, version, and conflict mutations only occur through the explicitly allowed failure-mode rule. `ErrRateLimited` must preserve the adapter-provided `RetryAfter` field in the service-visible error. `ErrNetworkUnavailable` must include the target stable id or remote alias and retry guidance. `ErrConflict` and `ErrRemoteCollision` must preserve local and remote payload evidence without applying either side automatically.

Add service-level tests that exercise the product path through `SyncToCache` using stubbed external GitCode responses and real cache behavior. Each test verifies returned typed error fields, user-visible message text, retry guidance, `RetryAfter` propagation, local/remote conflict payloads where applicable, sync-event final state, and post-failure cache state. Adapter-specific fixture tests remain with the adapter owner, but sync-engine tests verify that service orchestration never commits partial cache state after receiving typed adapter errors.
Acceptance Criteria: A developer triggers the failure suite by running `go test ./internal/service/... -run TestFailureModes`; each scenario calls the sync-engine `SyncToCache` product API and receives the expected typed error with prescribed visible message and recovery guidance. The timeout scenario verifies retries are exhausted or context cancellation occurs, the target record remains unchanged, failed sync-event evidence is present, and the error includes the record id plus retry suggestion. The rate-limit scenario verifies `RetryAfter` is present, `Retry-After` controls service-level delay when retrying is possible, and no partial page data is written after final failure. Additional scenarios cover partial JSON, auth expiry, remote id collision, cache corruption, lock contention, missing remote record, and oversized payload with executable cache-state assertions after each run.
Workload: 1.2 MM

## Cross-Cutting Constraints
- Routine reads remain cache-first and never trigger GitCode writes — the sync engine is only entered by explicit sync/write command paths, preserving the offline read invariant across CLI and MCP surfaces
- Cache mutation must be transactional at the sync-engine boundary — partial remote responses, rate limits, auth expiry, and timeouts must not leave partially reconciled source records visible to readers
- Stable source ids remain primary and remote ids are aliases — reconciliation must resolve through the identity map before inserting or updating records
- Idempotency keys must control replay behavior before lock acquisition — repeated successful sync calls with the same key must not duplicate mutations, acquire a lock, or call the remote
- HTTP pagination and transport classification stay in the GitCode adapter — service sync orchestration consumes complete adapter results and typed errors rather than duplicating adapter policy

## Data And Control Flow
- `GetSyncStatus` receives a stable id or alias — service sync engine queries cache identity and version metadata — no GitCode adapter call is allowed in this read path
- `SyncToCache` receives an explicit sync request — service sync engine checks successful idempotency replay before lock acquisition — replayed successful keys return prior results without lock, network, or mutation
- `SyncToCache` acquires a cache lock only for non-replayed work — service records an in-progress sync event, performs cache preflight, calls the adapter, stages complete normalized remote data, and reconciles in one transaction
- `internal/gitcode.Client` returns complete paginated results or typed errors — service decides whether to re-attempt the idempotent sync operation and preserves cache atomicity across attempts
- Adapter typed error reaches service sync engine — sync engine maps it to the prescribed visible error and cache-state guarantee — failed sync evidence is logged without committing partial source updates

## Component Interactions
- `internal/service sync engine` -> `internal/cache Store` — uses lock acquisition/release, identity lookup, source upsert, conflict upsert, remote version update, sync-event lookup/state transitions, integrity preflight, and transaction boundaries; cache remains the sole database writer
- `internal/service sync engine` -> `internal/gitcode Client` — calls context-aware sync/list methods only during explicit sync, receives complete paginated results or typed errors, and does not own HTTP pagination or low-level transport retry policy
- `internal/cli` -> `internal/service sync engine` — future `sync` and `sync-status` commands call `SyncToCache` and `GetSyncStatus` directly, matching the architecture’s shared service-layer contract

## Rationale
The sync-engine component is affected because the approved architecture assigns cache freshness, explicit refresh, reconciliation, lock contention, idempotency semantics, and degraded-network guarantees to the service orchestration layer. Component Impact deltas `sync-engine-delta-1` and `sync-engine-delta-2` require concrete implementation tasks for these missing service-layer behaviors.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0432-run_attempt-1/final_message.txt`
