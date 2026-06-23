# Design Package Component: cache-runtime

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Cache Runtime

## Summary
Cache runtime participates in iteration 4 by making live sync and live create-issue results durably inspectable through the configured cache path. The existing SQLite cache model is reused for issue, wiki, comment, identity, remote version, and sync-event state, while live create confirmations require an explicit cache-owned record.

## Top-Level Alignment
The approved architecture assigns `cache-runtime` ownership of persisted issue, wiki, comment, and create-confirmation state used by offline CLI integration tests. This design keeps the cache-first SQLite model and narrows changes to runtime state written by live sync and live write flows.

## Tasks

### Task 1: Cache confirmations persist
Outcome IDs: outcome-1, outcome-5, outcome-6, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-3, decommission-4
Change Type: change
Description: Extend the cache runtime contract so live sync records and live create-issue confirmations are inspectable without relying on fixture records or audit-only state. The component-local anchors are the SQLite `Store`, `SQLiteStore`, `RecordGraph`, `SyncGraph`, `Record`, `RecordComment`, `RemoteRevision`, and a new cache-owned create confirmation entity. This keeps existing issue/wiki/comment persistence but makes live write confirmation a first-class cache state associated with repository, remote alias, local record id, and idempotency reference.
Existing Behavior / Reuse: Reuse the existing SQLite cache schema, `UpsertSyncGraph`, `UpsertRecordGraph`, `GetRecord`, `ListRecords`, `RecordComment`, identity alias storage, remote version storage, and sync event storage. Existing behavior already persists remote issue/wiki records and comments through graph upserts, but create confirmation is only indirectly visible through refreshed records and audit state; implementation must confirm no cache-owned `CacheConfirmationRecord` equivalent already exists before adding one. Replace live-mode reliance on fixture-shaped `ISSUE-42`, `WIKI-HOME`, or audit-only confirmation with runtime-inspectable remote-provenance records plus explicit cache confirmation rows.
Detailed Design: Add a cache-owned `CacheConfirmationRecord` data class with `RepoID`, `ID`, `Command`, `RecordID`, `RecordType`, `RemoteType`, `RemoteID`, `IdempotencyKey`, `Status`, `SourceFingerprint`, and `CreatedAt`. Add `RecordCacheConfirmation(context.Context, CacheConfirmationRecord) error` and `GetCacheConfirmationByKey(context.Context, repoID, idempotencyKey string) (*CacheConfirmationRecord, error)` to the `Store` interface and implement them on `SQLiteStore` with idempotent upsert semantics keyed by `(repo_id, idempotency_key)`. Enforce non-empty `repo_id`, `record_id`, `remote_type`, `remote_id`, and `idempotency_key`; reject confirmations that do not point at an existing repository-local record graph. Add the backing SQLite table through the normal schema migration path, and include confirmation rows in cache counts or cache status only if an existing count surface can be extended without changing unrelated semantics. Update `UpsertSyncGraph` and `UpsertRecordGraph` invariants so live-provider records keep `ProvenanceRemote`, remote alias identities, comments, and remote revisions atomically in one transaction. The live create route records the refreshed `RecordGraph` first, then records `CacheConfirmationRecord` using the same record id, remote alias, and idempotency key. For decommission-3, live sync inserts or updates remote-provenance mock records through `SyncGraph`, and cache inspection fails if fixture ids are present for the live repo after live sync. For decommission-4, cache confirmation is produced only after confirmed provider write plus cache refresh; fixture read-only behavior remains internal to offline fixture writes and cannot create a live cache confirmation.
Acceptance Criteria: When a developer runs `go test ./...`, an offline CLI integration test triggers `gitcode-mcp sync --live` against the mocked GitCode API, then opens the configured cache runtime and observes mock issue, wiki, comment, identity, remote version, and sync event state while `ISSUE-42` and `WIKI-HOME` are absent for that live repo. When the operator path triggers `gitcode-mcp create-issue --live` with a valid test token and idempotency key, the cache runtime exposes a `CacheConfirmationRecord` for that key with the mock remote alias and refreshed record id, and `GetRecord` returns the corresponding remote-provenance issue. Re-running the same live create command with the same idempotency key returns the same cache confirmation without duplicating cache rows. Executable evidence is the stubbed-external-provider CLI integration test using `httptest.Server` plus cache-runtime tests for migration, upsert idempotency, record/comment inspection, and fixture-identifier rejection.
Workload: 1.2 MM

## Cross-Cutting Constraints
- Cache reads remain offline and inspectable — cache runtime is the durable evidence source for agents and CLI tests after live provider operations complete
- Live cache state is public-safe — persisted mock records, comments, aliases, and confirmations contain sanitized identifiers and no credentials or raw Authorization material
- Live mode is fail-closed — cache confirmation is written only after live provider confirmation and cache refresh, never from fixture read-only behavior

## Data And Control Flow
- Live sync response graph — `sync-service` creates `SyncGraph`; `cache-runtime` atomically persists `Record`, `RecordComment`, `Identity`, `RemoteRevision`, and `SyncEvent`; integration tests inspect through cache APIs
- Live create confirmation — `write-service` receives provider confirmation; `cache-runtime` persists `RecordGraph`; `cache-runtime` records `CacheConfirmationRecord` keyed by idempotency reference
- Offline acceptance evidence — `cli-integration-tests` run real CLI paths; `cache-runtime` is opened at the configured temporary cache path and queried for mock records and confirmation state

## Component Interactions
- `sync-service` -> `cache-runtime` — submits live issue/wiki/comment `SyncGraph` records that must commit atomically with remote aliases and sync events
- `write-service` -> `cache-runtime` — submits refreshed create-issue `RecordGraph` and a cache confirmation keyed by idempotency reference after live provider confirmation
- `cli-integration-tests` -> `cache-runtime` — inspects configured temporary cache state to prove mock records are present, fixture records are absent, and create confirmation is durable

## Rationale
`cache-runtime` is affected because iteration 4 acceptance depends on observable runtime state, not just CLI output or provider request counts. The existing cache graph model should be reused for live issue/wiki/comment records, while create confirmation needs a cache-owned inspectable record so Task 6 evidence is not dependent on audit-runtime alone.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0251-run_attempt-1/final_message.txt`
