# Scenarios: Add Schema and Search Migration

## 004-cache-store-task-2-add-schema-and-search-migration-scenario-1

A developer runs `go test ./internal/cache/... -run 'TestSchemaVersion|TestInitialMigration|TestSearchFallbackParity|TestFTSAvailability'`; the cache migration surface opens an in-memory SQLite database through `NewSQLiteStore`, reports the supported schema version, lists the required application tables and indexes, and confirms the `chunks.embedding` column is nullable with a NULL default.

Executable evidence: `run.sh` invokes `go test ./internal/cache/... -run 'TestSchemaVersion|TestInitialMigration|TestSearchFallbackParity|TestFTSAvailability' -count=1`. The tests exercise the production cache migration runner, schema inspection, FTS detection, and store startup path without network access.

## 004-cache-store-task-2-add-schema-and-search-migration-scenario-2

The cache tests insert two chunks for one source/content hash at different byte starts, verify both rows exist, and verify duplicate byte-start insertion follows the Store contract by being rejected through the `(source_id, content_hash, byte_start)` uniqueness constraint.

Executable evidence: `run.sh` invokes `go test ./internal/cache/... -run 'TestInitialMigration|TestChunkSchemaEmbeddingColumn|TestChunkIdentity' -count=1`. The tests exercise `UpsertSourceGraph`, `UpsertChunk`, `GetChunks`, deterministic chunk ids, nullable embeddings, and duplicate byte-start enforcement through the in-memory SQLite store.

## 004-cache-store-task-2-add-schema-and-search-migration-scenario-3

The tests insert identical source data into an FTS-enabled store and a forced-fallback store, run `SearchSources` with the same query, and verify equivalent visible result ids, deterministic ordering, snippet fields, kind filtering, limits, and JSON-serializable `SearchResult` output shape.

Executable evidence: `run.sh` invokes `go test ./internal/cache/... -run TestSearchFallbackParity -count=1`. The test exercises the production `SearchSources` abstraction through both runtime search routes and fails if visible search results diverge.

## 004-cache-store-task-2-add-schema-and-search-migration-scenario-4

Re-running the migration runner on the same database exits successfully without duplicating schema rows or corrupting existing data.

Executable evidence: `run.sh` invokes `go test ./internal/cache/... -run TestSchemaVersion -count=1`. The test opens an in-memory store, calls the production migration runner a second time against the same `*sql.DB`, and verifies the schema version remains the expected current version.
