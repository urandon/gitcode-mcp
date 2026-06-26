# Validation Scenarios: 021-internal-cache-task-2-add-schema-version-check-against-version-11-inter

## 021-internal-cache-task-2-add-schema-version-check-against-version-11-inter-scenario-1

Cache file with schema_version > 11: open returns schema_incompatible diagnostic.

Concrete offline validation:

1. Run the production cache runtime test `TestCheckVersionCompatibilityFuture` in `internal/cache`. The test creates a temporary database, sets the schema version to `currentSchemaVersion + 1`, calls `CheckVersionCompatibility`, and asserts `compat.Compatible == false`, `compat.PermitWrites == false`, the message contains "newer than supported", and the remediation contains "upgrade".
2. Run the production cache test `TestNewSQLiteStoreFutureSchemaBlocked` in `internal/cache`. The test creates a file-backed temporary cache at `currentSchemaVersion + 1` and asserts `NewSQLiteStore` returns `ErrSchemaVersionIncompatible` with a message referencing "binary" or "upgrade".
3. Run the production cache test `TestReadOnlyStoreRejectsFutureVersion` in `internal/cache/migrate_test.go`. The test creates a file-backed temporary cache at a future version and asserts `NewSQLiteReadOnlyStore` returns `ErrSchemaVersionIncompatible`.
4. Run the production MCP test `TestStartupDiagnosticSchemaIncompatible` in `internal/mcp`. The test creates a `SchemaVersionError` for a future version, calls `StartupDiagnosticFromError`, and asserts the `ErrorClass` field equals `"schema_incompatible"`.
5. Run the production end-to-end test in `cmd/gitcode-mcp` that creates a cache file with `schema_version = 99`, starts the MCP server, and asserts `tools/list` returns `schema_incompatible` and calling `doctor` returns `schema_incompatible` with `"upgrade"` remediation.

## 021-internal-cache-task-2-add-schema-version-check-against-version-11-inter-scenario-2

Cache file with schema_version 11: opens normally.

Concrete offline validation:

1. Run the production cache runtime test `TestCheckVersionCompatibilityCurrent` in `internal/cache`. The test creates a fresh database at `currentSchemaVersion`, calls `CheckVersionCompatibility`, and asserts `compat.Compatible == true`, `compat.PermitWrites == true`, and `compat.DetectedVersion == currentSchemaVersion`.
2. Run the production cache runtime test `TestSchemaVersion` in `internal/cache`. The test creates a new SQLiteStore, reads the schema version via `PRAGMA user_version`, and asserts it equals `currentSchemaVersion` (11). The test also re-runs `runMigrations` and asserts it is idempotent (no error on re-run).
3. Verify that `currentSchemaVersion` constant is 11 by inspecting the compiled test output of `TestSchemaVersion`, which asserts exact equality.
4. Run `go test ./internal/cache/...` and verify the full cache test suite passes with exit code 0.

## 021-internal-cache-task-2-add-schema-version-check-against-version-11-inter-scenario-3

Cache file with version < 11: existing migration path used. currentSchemaVersion constant is 11.

Concrete offline validation:

1. Run the production cache runtime test `TestCheckVersionCompatibilityVersionTwoSuggestsMigration` in `internal/cache`. The test creates a database at version 2, calls `CheckVersionCompatibility`, and asserts `compat.Compatible == true`, `compat.PermitWrites == false`, and the remediation contains `migrate-cache`.
2. Run the production cache test `TestNewSQLiteStoreVersionTwoBlockedWithMigrateHint` in `internal/cache`. The test creates a file-backed cache at version 2, calls `NewSQLiteStore`, and asserts it returns `ErrSchemaVersionIncompatible` with `detected == 2`, `expected == currentSchemaVersion`, and a message containing `migrate-cache`.
3. Run the production cache test `TestReadOnlyStoreAcceptsVersionTwo` in `internal/cache/migrate_test.go`. The test creates a cache at version 2, opens it with `NewSQLiteReadOnlyStore`, and asserts it opens successfully (read-only accepts migratable caches since `Compatible == true`).
4. Run the production cache migration test `TestMigrateFromVersion2ToVersion4` in `internal/cache`. The test migrates a version 2 cache to version 4, proving the existing migration path works for versions below 11.
5. Run `TestMigrateFromCurrentVersionNoOp` in `internal/cache`. The test migrates a version 11 cache, which is a no-op (current version needs no migration).
