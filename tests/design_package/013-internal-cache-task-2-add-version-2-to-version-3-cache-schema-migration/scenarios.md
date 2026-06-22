# Scenario 013: Version-2-to-current cache schema migration

All validation is offline and deterministic. The scenarios create temporary SQLite cache files, run the real `gitcode-mcp migrate-cache` CLI through `go run ./cmd/gitcode-mcp`, and inspect the resulting database state. No GitCode network access, credentials, keychain access, or live providers are used.

## 013-internal-cache-task-2-add-version-2-to-version-3-cache-schema-migration-scenario-1

`gitcode-mcp migrate-cache` against iter-2 cache → schema upgraded in place, data preserved, PRAGMA user_version set to the current schema version. `gitcode-mcp migrate-cache` against iter-1 cache → incompatibility, no migration attempted.

### Product scenario A: iter-2 compatible cache migration

- **Given** a temporary cache database with `schema_version.version = 2`, legacy v2-shaped `chunks`, `snapshots`, and `snapshot_chunks` tables, and sentinel rows in `repos`, `sources`, `records`, and `chunks`
- **And** SQLite `PRAGMA user_version` is initialized to `2`
- **When** the real CLI command `go run ./cmd/gitcode-mcp migrate-cache --cache-path <path> --confirm --format json` is executed
- **Then** the command exits `0`
- **And** the CLI reports `status: migrated` and a backup path
- **And** the cache remains in place with all sentinel data preserved
- **And** migration columns required by the current production migrations exist
- **And** the application `schema_version` table reaches the current production schema version
- **And** `PRAGMA user_version` reaches the current production schema version

### Product scenario B: iter-1 incompatible cache rejection

- **Given** a temporary pre-schema-versioning cache database with user data but no `schema_version` table
- **When** the real CLI command `go run ./cmd/gitcode-mcp migrate-cache --cache-path <path> --confirm --format json` is executed
- **Then** the command exits non-zero
- **And** the CLI reports `status: incompatible` and a re-initialization remediation
- **And** no `schema_version` table is created
- **And** the legacy sentinel row is preserved
- **And** no backup file is created for the incompatible cache

### Product failure reported by this validation

No product failures are reported by the current validation run.
