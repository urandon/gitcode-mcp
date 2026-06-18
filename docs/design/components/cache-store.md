# Component Design: Local Cache Store

## Summary
The `cache-store` component must be added as `internal/cache` to own durable SQLite cache state, schema migrations, record accessors, chunk/provenance persistence, sync metadata, conflicts, integrity checks, and lock files. It is the sole database writer boundary used by higher layers.

## Top-Level Alignment
`cache-store` implements architecture responsibility `a1`: SQLite schema ownership, cache-first reads, stable id and alias resolution, lock-file acquisition/release, and cache integrity. It supports request tasks 2, 6, 9, and 14 through component-local Store APIs and tests, while service, CLI, MCP, and index consume it indirectly.

## Tasks

### Task 1: Add Store source graph API
Outcome IDs: outcome-2, outcome-6, outcome-9
Outcome Role: primary_product
Decommission IDs: decommission-4
Change Type: add
Description: Add the `internal/cache` package as the local cache ownership boundary. The package owns the `Store` interface, SQLite implementation, source/link/identity/chunk/sync/conflict record models, and all database writes used by higher layers. This task replaces local markdown path links as the primary cross-reference mechanism with stable ids and alias-backed cache records.
Existing Behavior / Reuse: `internal/cache` is currently absent, and durable cache-backed read state does not exist in the scaffold. Reuse the existing Go module, `modernc.org/sqlite` dependency, Go 1.22 conventions, and the public-safe repository boundary; leave CLI and MCP access to future service-layer consumers rather than direct SQLite access.
Detailed Design: Add a `Store` interface with methods `UpsertSourceGraph`, `UpsertSource`, `GetSource`, `ListSources`, `SearchSources`, `UpsertIdentity`, `GetIdentityMap`, `ResolveAlias`, `UpsertLink`, `GetBacklinks`, `UpsertChunk`, `GetChunks`, `RecordSyncEvent`, `GetSyncStatus`, `UpsertConflict`, `GetConflicts`, `IntegrityCheck`, `AcquireLock`, `ReleaseLock`, and `Close`. Add record structs for `Source`, `SourceFilter`, `SearchQuery`, `SearchResult`, `Identity`, `RemoteAlias`, `Link`, `Chunk`, `SyncEvent`, `SyncStatus`, `Conflict`, and `SourceGraph`; all public cache records use stable source ids as primary identifiers and treat remote ids and local paths as aliases. Implement `SQLiteStore` over `*sql.DB`, enable foreign keys, run migrations at construction, and compute deterministic chunk row ids from `source_id + content_hash + byte_start`; `(source_id, content_hash)` identifies the parent source version but is not unique in `chunks`, while chunk row uniqueness is enforced by deterministic `id` and by `(source_id, content_hash, byte_start)`. Add `UpsertSourceGraph(ctx, graph SourceGraph)` as the cache-local unit-of-work for atomic ingest/sync graph updates: it opens one SQL transaction, upserts the source, identities, links, chunks, remote version, sync event, and conflicts, then commits only after all writes succeed; any error rolls back and leaves no partial graph visible. Enforce `decommission-4` by resolving product cross-references through `identity_map` and `links`; raw relative markdown paths remain source metadata and are not valid backlink keys.
Acceptance Criteria: A developer triggers `go test ./internal/cache/... -run 'TestBacklinks|TestChunkIdentity|TestIdentityResolution|TestSourceGraphRollback'`; the `internal/cache` API opens an in-memory SQLite store, inserts source and task records through `UpsertSourceGraph`, stores an identity alias, inserts a link, queries backlinks by target id, and receives the correct source record with stable id, path, and alias data. The same test inserts at least two chunks for the same `source_id` and `content_hash` with different byte offsets, verifies both persist with deterministic ids, and verifies repeated upsert is idempotent. The rollback test injects a failing link or chunk in `UpsertSourceGraph` and verifies no source, identity, link, chunk, sync, or conflict row from that graph is visible afterward. A second trigger through `go test ./...` shows higher packages still compile with no circular imports after adding `internal/cache`.
Workload: 2.0 MM

### Task 2: Add schema and search migration
Outcome IDs: outcome-2, outcome-9
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: add
Description: Add the initial migration set and schema version runner owned by `internal/cache`. The schema persists normalized sources, full-text data, identity mappings, links, remote revisions, sync events, conflicts, and chunks. It also defines one `SearchSources` abstraction whose visible result shape is stable whether SQLite FTS5 or the LIKE fallback is used.
Existing Behavior / Reuse: No migration directory, schema version table, SQLite store implementation, or cache search abstraction exists today. Reuse the approved table names and `modernc.org/sqlite`; do not introduce a separate migration framework.
Detailed Design: Add a migration runner that loads ordered SQL migrations, checks a single-row `schema_version` table, executes missing migrations inside a transaction, and refuses startup on unknown future schema versions. Add initial DDL for `sources`, `fts_index` using FTS5 when available, `identity_map`, `links`, `remote_revisions`, `sync_events`, `conflicts`, and `chunks`; include indexes for source kind/status, alias lookup, link target lookup, sync event source lookup, and chunk source lookup. The `chunks` table stores `id`, `source_id`, `content_hash`, `byte_start`, `byte_end`, `line_start`, `line_end`, `heading_path`, `text`, `normalized_text`, `inherited_metadata`, `outbound_links`, `resolved_aliases`, and nullable `embedding` defaulting to `NULL`; it enforces uniqueness on `id` and `(source_id, content_hash, byte_start)`, while allowing multiple chunks for one `(source_id, content_hash)` parent version. Add FTS availability detection during store startup and route `SearchSources` through either FTS5 or LIKE fallback behind the same `SearchResult` contract: results contain `id`, `path`, `title`, `snippet`, `score`, and `line`, are ordered by descending relevance score then stable id/path tie-breakers, and produce deterministic snippets by selecting the first matching normalized body/title window with the same max length in both paths.
Acceptance Criteria: A developer triggers `go test ./internal/cache/... -run 'TestSchemaVersion|TestInitialMigration|TestSearchFallbackParity|TestFTSAvailability'`; the cache migration surface creates an in-memory database, reports the expected schema version, lists all required tables, and confirms the chunk schema includes nullable `embedding`. The tests insert two chunks for one source/content hash at different byte starts and verify both rows exist while duplicate byte-start insertion is idempotent or rejected according to the Store upsert contract. The tests insert identical source data into FTS-enabled and forced-fallback stores, run `SearchSources` with the same query, and verify equivalent visible result ids, deterministic ordering, snippet fields, and JSON-serializable `SearchResult` shape. Re-running the migration runner on the same database exits successfully without duplicating schema rows or corrupting existing data.
Workload: 1.2 MM

### Task 3: Add cache lock handle
Outcome IDs: outcome-6, outcome-13
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: add
Description: Add non-blocking cache lock ownership to the store so sync and write flows can safely serialize database mutation. The lock is a runtime entity owned by `internal/cache` and exposed as a small handle returned by `AcquireLock`. This supplies supporting failure-mode evidence for concurrent writer behavior.
Existing Behavior / Reuse: No cache lock, typed lock error, or lock-aware sync-state store exists today. Reuse Go file handles and Unix `flock` semantics specified by the approved architecture; keep the lock independent of CLI command parsing.
Detailed Design: Add `LockHandle` and `ErrLockContention` types to `internal/cache`; `AcquireLock(ctx, lockPath)` opens or creates the configured lock file, attempts an exclusive non-blocking lock, and returns `ErrLockContention{Path, HolderHint}` immediately when another process holds it. `ReleaseLock` unlocks exactly once, closes the file, and is safe to call from deferred cleanup after partial sync failure. Lock enforcement is explicit: cache only owns lock primitives and does not require a `LockHandle` for ordinary Store mutations; the service sync/write state machine must acquire the lock before calling `UpsertSourceGraph` for remote sync or write reconciliation, and if `AcquireLock` returns `ErrLockContention`, those mutation calls must not be attempted. Cache tests model this ownership rule by acquiring the lock, attempting a second acquisition, and verifying the simulated sync mutation path exits before invoking graph upsert; contention leaves SQLite application tables unchanged except for normal driver-managed journaling.
Acceptance Criteria: A developer triggers `go test ./internal/cache/... -run 'TestLockContention|TestLockContentionBlocksSimulatedSync'`; the cache lock API acquires a lock file, attempts a second acquisition, receives typed `ErrLockContention` with the lock path, releases the first handle, and then successfully acquires again. The simulated sync test holds the lock, attempts the service-style lock-before-mutate path, verifies `UpsertSourceGraph` is not called while `ErrLockContention` is active, and confirms through `GetSyncStatus` and source row counts that no partial data was written. The executable evidence is local cache tests only, with no network access.
Workload: 0.6 MM

### Task 4: Add minimum cache state test
Outcome IDs: outcome-14
Outcome Role: primary_product
Decommission IDs: decommission-14
Change Type: remove
Description: Provide the cache-store behavior required for the Day 7 minimum replacement walkthrough. This removes the cache-store blocker for the legacy coordinator intake path by making ingest/search/get/backlinks/sync-status state durable and offline-readable. The product effect is cache runtime behavior, not a standalone plan document.
Existing Behavior / Reuse: The current repository has no durable cache for the Day 7 walkthrough, and the CLI scaffold cannot read ingested source state. Reuse the Store API, `UpsertSourceGraph`, schema, search abstraction, identity resolution, links, and sync-status records from prior tasks; do not create a parallel walkthrough-only data model.
Detailed Design: Add cache-level fixture helpers and integration tests that exercise the same Store methods future service/CLI flows use: source upsert for ingest, `SearchSources`, `ListSources`, `GetSource`, `GetBacklinks`, `ResolveAlias`, and `GetSyncStatus`. Persist enough source metadata for coordinator intake, task lookup, and handoff review: stable id, kind, title, path, body, status, labels, content hash, timestamps, links, aliases, and remote freshness fields. Enforce `decommission-14` by making the product runtime route depend on cache tables after ingest; markdown files are inputs to ingest only and hand-maintained markdown indexes are not queried by cache reads. The cache-store handoff to service is a deterministic set of Store calls and state transitions that complete without invoking network adapters.
Acceptance Criteria: A developer triggers `go test ./internal/cache/... -run TestMinimumReplacementCacheState`; the cache API ingests fixture-like source records into an in-memory store, searches for `backlog`, lists ready task records, retrieves `DOC-123`, resolves backlinks for `DOC-123`, and reports sync status without any network dependency. The expected visible/state outcome is that each Store call returns the data needed by the future CLI/MCP Day 7 route and the test fails if any result depends on shell-readable markdown indexes rather than cache tables. Running `go test ./... -short` includes this cache scenario as executable evidence for the broader Day 7 walkthrough.
Workload: 0.5 MM

## Cross-Cutting Constraints
- SQLite remains the single cache writer boundary — service, index, CLI, and MCP must go through `internal/cache.Store` so reads remain cache-first and writes remain auditable.
- Stable source ids are primary identities — remote GitCode ids and local paths are aliases, not backlink keys, preserving deterministic resolution across offline workflows.
- Multi-table cache mutation is transactional through `UpsertSourceGraph` — source, identity, link, chunk, sync, and conflict rows must commit or roll back as one visible graph update.
- Sync/write lock contention prevents mutation attempts — cache owns `AcquireLock`/`ReleaseLock`, and service must not call graph mutation after `ErrLockContention`.
- Chunk provenance is stored before RAG integration — byte offsets, line ranges, heading paths, hashes, metadata, outbound links, and alias resolutions must be queryable without embeddings.
- Search output is transport-stable — FTS5 and LIKE fallback must return the same `SearchResult` shape and deterministic ordering/snippet semantics for CLI and MCP equivalence.

## Data And Control Flow
- Migration startup — `NewSQLiteStore` opens SQLite, enables foreign keys, checks `schema_version`, runs missing migrations, detects FTS availability, then exposes Store methods — migration must finish before any accessor is usable.
- Ingest/cache write — service or tests call `UpsertSourceGraph` with source, identities, links, chunks, and freshness metadata — `internal/cache` owns the SQL transaction and updates search/index tables consistently.
- Offline read — service, CLI, or MCP call `SearchSources`, `GetSource`, `ListSources`, `GetBacklinks`, `ResolveAlias`, or `GetSyncStatus` — calls read only SQLite and never invoke GitCode network adapters.
- Search execution — `SearchSources` selects FTS5 or LIKE at runtime — both paths normalize query text, compute deterministic snippets, and apply the same visible ordering contract.
- Sync mutation — service acquires a cache lock, compares remote version data, calls `UpsertSourceGraph`, records sync events, then releases the lock — lock contention returns typed cache error before mutation is attempted.

## Component Interactions
- `internal/cache.Store` -> `internal/service` — service receives cache-first methods for search, get, list, backlinks, resolve, sync status, sync event logging, and conflict persistence; service owns orchestration but not SQL.
- `internal/cache.Store` -> `internal/index` — index reads sources and writes derived links/chunks through Store methods; index owns parsing and chunking algorithms while cache owns persistence invariants.
- `internal/cache.LockHandle` -> `internal/service.SyncToCache` — sync obtains non-blocking mutual exclusion before remote fetch reconciliation and releases it after committing or aborting.
- `internal/cache` -> `internal/cli` and `internal/mcp` through `internal/service` — transport packages never access SQLite directly, preserving the shared service contract and equivalent CLI/MCP behavior.

## Rationale
The approved architecture marks `cache-store` as detailed and assigns it concrete ownership of SQLite schema, accessors, chunk storage, sync metadata, conflicts, integrity, and locks. Existing source functionality is absent for this component, so the component requires new implementation tasks derived directly from the cache-store deltas.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0420-run_attempt-1/final_message.txt`
