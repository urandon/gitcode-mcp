# Design Package Component: cache-sync

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Cache & Sync Bootstrap

## Summary
Cache & Sync Bootstrap must evolve the current source-centric SQLite scaffold into the repo-scoped durable cache required for dogfood. The component owns schema migration, cache-first query contracts, sync persistence, provenance, snapshots/audit storage, and SQLite reader/writer admission.

## Top-Level Alignment
This component implements the approved architecture’s SQLite WAL cache, sync bootstrap, provenance tagging, idempotency keys, sync events, audit trail, snapshot tables, and cache concurrency rules. It provides the storage/runtime contract consumed by repo binding, GitCode adapter, CLI reads/writes, MCP reads, index/chunking, and snapshot/diff.

## Tasks

### Task 1: Repo-scoped cache schema migration
Outcome IDs: outcome-2, outcome-4, outcome-8, outcome-10, outcome-11
Outcome Role: primary_product
Decommission IDs: decommission-4, decommission-6
Change Type: add
Description: Add the durable repo-scoped cache model owned by Cache & Sync Bootstrap. The current cache stores global `sources`, global `identity_map` aliases, `chunks`, `remote_revisions`, `sync_events`, and `conflicts`; it does not yet contain `repos`, `records`, `record_comments`, repo-scoped aliases, provenance, audit rows, snapshots, or snapshot chunk membership.
Existing Behavior / Reuse: Reuse the existing `SQLiteStore`, migration runner, scan helpers, JSON marshal helpers, source/link/chunk access patterns, and cache error normalization. Replace product reliance on globally unique aliases by adding repo-scoped identity resolution while keeping any old source-centric concepts only as migration compatibility or test fixture input until callers move to `records`.
Detailed Design: Add a schema migration that creates `repos`, `records`, `record_comments`, `identity_map`, `links`, `remote_revisions`, `sync_events`, `audit_trail`, `snapshots`, and `snapshot_chunks` with `repo_id` on every repo-owned row and foreign keys back to `repos`. Add cache model structs `Repo`, `Record`, `RecordComment`, `IdentityAlias`, `RemoteRevision`, `SyncEvent`, `AuditTrailEntry`, `Snapshot`, and `SnapshotChunk`, and add store methods such as `UpsertRepo`, `GetRepo`, `UpsertRecordGraph`, `GetRecord`, `ListRecords`, `SearchRecords`, `ResolveRepoAlias`, `RecordCounts`, `UpsertSnapshot`, and `ListSnapshotChunks`. `identity_map` must use a repo-scoped uniqueness invariant like `(repo_id, alias_type, alias)` and remote identity uniqueness like `(repo_id, remote_type, remote_id)`, while `ResolveRepoAlias` must require `repo_id` and return a typed conflict if a caller attempts unscoped alias resolution. `records.provenance` must be constrained to `remote`, `projection`, or `bridge`; `remote` records are canonical for GitCode issue/wiki identities, and `projection` rows cannot overwrite remote identity columns unless an explicit import/write path passes a separate cache method that records the transition in `audit_trail`. `snapshot_chunks` must link stored snapshots to chunk ids and citation/source ranges rather than recomputing current state. Remove, disable in product runtime, or keep internal-only any path that resolves aliases globally across repositories; enforce the negative invariant with store tests that fail if `issue:42` can resolve without a `repo_id`. Replace fixture-only active cache emptiness by making migrated cache status expose real repo-scoped row counts for records, comments, identities, events, audit rows, snapshots, and chunks.
Acceptance Criteria: A developer runs fixture-backed migrations and the product surface `gitcode-mcp cache-status --repo <repo_id>` against a temporary SQLite cache; the CLI reports WAL-capable cache state plus repo-scoped row counts for records, comments, identity aliases, sync events, audit rows, snapshots, and chunks, and a two-repository fixture proves colliding aliases resolve only with `repo_id` or return a typed conflict. Executable evidence is Go migration/store tests plus a CLI cache-status integration test using sanitized fixtures.
Workload: 2.5 MM

### Task 2: Sync graph upsert workflow
Outcome IDs: outcome-4, outcome-10
Outcome Role: primary_product
Decommission IDs: decommission-4
Change Type: add
Description: Add the sync bootstrap persistence workflow that turns adapter issue/wiki payloads into canonical cache records. Cache & Sync owns the transactional graph upsert, sync event idempotency, remote version persistence, and provenance separation used after the CLI sync product path runs.
Existing Behavior / Reuse: Reuse the existing `UpsertSourceGraph` transaction shape, `RecordSyncEvent`, `GetSyncEventByKey`, remote version pattern, link upserts, and chunk upsert behavior. Confirmed absent are repo-scoped sync idempotency, issue/wiki comment persistence, canonical remote-vs-projection provenance rules, and a product cache path that guarantees nonempty offline reads after fixture sync.
Detailed Design: Add a component-local `SyncGraph` or extend the graph model to carry `repo_id`, `Record`, `RecordComment` entries, aliases, links, optional chunks from the index component, `RemoteRevision`, and one or more `SyncEvent` rows. Implement `UpsertSyncGraph(ctx, graph)` as a single transaction that validates the repo exists, checks an idempotency key scoped by `(repo_id, operation, remote_type, remote_id, remote_revision)`, upserts remote records/comments/revisions, upserts aliases, inserts sync events, and optionally writes chunks produced by the index path. The algorithm must treat remote issue/wiki records as canonical: when a remote record matches a projection alias, preserve the projection row or provenance metadata but do not promote projection-only aliases to remote identity unless the adapter payload supplies that remote id. Failed or partial adapter fetches must not commit a success event; either commit a failure `sync_event` with an error class and no misleading cache mutation, or return a typed error before product callers report success. The workflow replaces fixture-only cache bootstrap by making fixture adapter sync populate real `records`, `record_comments`, `identity_map`, `sync_events`, and index chunk rows that offline CLI and MCP reads can query.
Acceptance Criteria: A developer triggers the product surface `gitcode-mcp sync --repo <repo_id> --issues --wiki --index` with sanitized fixture adapter data, disables network, then runs the CLI read command paths `gitcode-mcp search --repo <repo_id>`, `gitcode-mcp get --repo <repo_id> <record>`, and `gitcode-mcp get-snippet --repo <repo_id> <record>`; the visible CLI responses return at least one issue and one wiki record from the SQLite cache with remote provenance, comments where present, sync events, and chunks. Executable evidence is a fixture-backed CLI integration test exercising those command paths plus store tests proving idempotent repeat sync and import-then-sync provenance preservation.
Workload: 2.0 MM

### Task 3: WAL writer ownership runtime
Outcome IDs: outcome-8
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Add the runtime admission model for one shared SQLite cache with many readers and serialized writers. Cache & Sync owns WAL activation, busy timeout, writer ownership, migration exclusion, typed busy/owned errors, and checkpoint hooks used by sync/index/write and MCP read concurrency.
Existing Behavior / Reuse: Reuse the existing `AcquireLock`/`ReleaseLock` file-lock concept and `ErrLockContention` as the seed for typed lock errors. Confirmed absent are `PRAGMA journal_mode=WAL`, configured busy timeout, a lock owner record with operation/start time, migration blocking under another process owner, and checkpoint behavior after write-heavy operations.
Detailed Design: Change `NewSQLiteStore` initialization to enable foreign keys, `journal_mode=WAL`, `busy_timeout`, and a bounded connection policy suitable for concurrent readers. Add a cache-owned `WriterAdmission` API such as `AcquireWriter(ctx, operation, repoID)` returning a `WriterLease` with operation, repo_id, start time, process id, and cache path; implement it using the existing file lock plus an in-database or lock-file metadata record so typed busy errors can report the active operation start time. Require schema migrations to acquire a migration writer lease before running and return a typed owned/busy error if another process owns the cache; regular readers must not need this lease. Sync, index, and write product paths must acquire the writer lease before mutating cache tables, while read methods continue using WAL shared reads. Add `Checkpoint(ctx, reason)` for sync completion and server shutdown to run a safe WAL checkpoint without blocking active readers longer than the configured busy timeout.
Acceptance Criteria: A system test triggers the cache runtime path by opening one temporary SQLite cache, starts two reader clients through the service/MCP-equivalent read path, then holds a sync/index writer lease and attempts a second writer plus a migration; reader queries continue where SQLite permits, the second writer receives a typed busy/owned error including operation and start time, and migration is blocked until the lease is released. Executable evidence is Go concurrency/runtime tests using production cache locking code and a temporary SQLite database.
Workload: 1.5 MM

## Cross-Cutting Constraints
- Every cache row and read response must carry or derive `repo_id` — repo binding, CLI, MCP, sync, and snapshots all depend on the same scope key
- Routine reads must remain cache-first and offline after sync — this component must not introduce network reads or adapter calls into cache query methods
- Remote GitCode issue/wiki records are canonical while local markdown is projection/bridge provenance — identity resolution and upserts must preserve source-of-truth semantics
- SQLite remains the sole cache engine with pure-Go driver expectations — schema, concurrency, and migrations must stay within SQLite/modernc behavior
- Writers serialize and readers continue through WAL where safe — MCP multi-client reads and CLI reads depend on shared-cache admission rules

## Data And Control Flow
- Configured repo is inserted before cache use — repo binding owner calls `UpsertRepo`, then all cache writes and reads require `repo_id` filters
- Sync command fetches adapter payloads, then cache owns transactional persistence — adapter produces issue/wiki data; `UpsertSyncGraph` persists records, comments, aliases, revisions, and events before index chunks are attached
- Projection import writes provenance-separated records — import caller writes `projection` or `bridge`, and later remote sync may add canonical `remote` records without promoting projection-only aliases
- Snapshot export resolves stored state — snapshot/diff caller reads `snapshots` and `snapshot_chunks`; unknown ids return not-found instead of falling back to current cache state
- Writer operations acquire ownership before mutation — sync/index/write/migration acquire `WriterLease`; CLI/MCP reads use normal read transactions under WAL

## Component Interactions
- `repo_binding` -> `cache-sync` — repo add/status uses `UpsertRepo`, `GetRepo`, `RecordCounts`, and repo-scoped alias checks; cache rejects unscoped alias collisions
- `gitcode_adapter` -> `cache-sync` — adapter payloads are transformed into `SyncGraph` records/comments/revisions/events; cache persists only sanitized domain data and idempotency keys
- `index_chunking` -> `cache-sync` — index builder supplies deterministic chunks with source ranges; cache stores chunks and `snapshot_chunks` under `repo_id` and record ids
- `cli_read` -> `cache-sync` — CLI read commands query repo-scoped records, search rows, backlinks, stale state, cache status, sync status, and snapshots without network
- `mcp_server` -> `cache-sync` — MCP tools share the same read methods as CLI and rely on WAL concurrency for multiple clients
- `cli_write` -> `cache-sync` — successful live writes persist audit rows and refreshed cache state only after adapter confirmation; unavailable adapters cannot create success audit entries
- `snapshot_diff` -> `cache-sync` — snapshot export/diff reads stored snapshot ids and chunk citations; cache returns typed not-found for unknown snapshot ids

## Rationale
Cache & Sync is materially affected because the current cache scaffold is global-source oriented and lacks the approved repo-scoped schema, sync bootstrap, provenance, audit/snapshot tables, WAL setup, and writer admission model. These changes are component-local foundations required before CLI, MCP, adapter, index, write, and snapshot components can satisfy their own contracts.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0210-run_attempt-1/final_message.txt`
