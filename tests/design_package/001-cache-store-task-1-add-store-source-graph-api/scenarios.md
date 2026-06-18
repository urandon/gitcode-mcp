# Validation Scenarios: 001-cache-store-task-1-add-store-source-graph-api

## 001-cache-store-task-1-add-store-source-graph-api-scenario-1
A developer triggers `go test ./internal/cache/... -run 'TestBacklinks|TestChunkIdentity|TestIdentityResolution|TestSourceGraphRollback'`; the `internal/cache` API opens an in-memory SQLite store, inserts source and task records through `UpsertSourceGraph`, stores an identity alias, inserts a link, queries backlinks by target id, and receives the correct source record with stable id, path, and alias data.

Executable evidence: `run.sh` invokes the exact focused cache test trigger. `TestBacklinks` validates in-memory SQLite graph upsert, identity alias persistence, stable-id link insertion, stable target-id backlink lookup, and returned source id/path/alias data.

## 001-cache-store-task-1-add-store-source-graph-api-scenario-2
The same test inserts at least two chunks for the same `source_id` and `content_hash` with different byte offsets, verifies both persist with deterministic ids, and verifies repeated upsert is idempotent.

Executable evidence: `run.sh` invokes the focused cache test trigger. `TestChunkIdentity` validates multiple chunks for one source/content hash at different byte offsets, deterministic chunk ids, and idempotent repeated graph upsert.

## 001-cache-store-task-1-add-store-source-graph-api-scenario-3
The rollback test injects a failing link or chunk in `UpsertSourceGraph` and verifies no source, identity, link, chunk, sync, or conflict row from that graph is visible afterward.

Executable evidence: `run.sh` invokes the focused cache test trigger. `TestSourceGraphRollback` validates transaction rollback after a failing graph write and checks that source, identity, link/backlink, chunk, sync status, and conflict data from the failed graph are not visible.

## 001-cache-store-task-1-add-store-source-graph-api-scenario-4
A second trigger through `go test ./...` shows higher packages still compile with no circular imports after adding `internal/cache`.

Executable evidence: `run.sh` invokes `go test ./...` after the focused cache tests. This exercises all packages offline and fails on compile errors, circular imports, or broken higher-package tests.
