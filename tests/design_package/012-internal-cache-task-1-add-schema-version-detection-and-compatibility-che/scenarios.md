# Scenario 012: Schema version detection and compatibility checks

All validation exercises below are offline/deterministic Go tests with no network access, no live GitCode API calls, and no OS keychain. The only external dependency is the filesystem for temporary SQLite databases. The production `internal/cache` package code paths are exercised directly.

## 012-internal-cache-task-1-add-schema-version-detection-and-compatibility-che-scenario-1

Iter-2 cache opened with iter-3 binary → schema version mismatch reported with detected and expected version, actionable 'migrate-cache' message.

- **Given** a cache database with `schema_version.version = 2` (matching iter-2 schema)
- **When** `CheckVersionCompatibility` is called against the database where `currentSchemaVersion = 4`
- **Then** `Compatible` is `true` (migration is possible)
- **And** `PermitWrites` is `false` (writes blocked until migration)
- **And** `Message` describes the detected and expected versions
- **And** `Remediation` contains the string `migrate-cache`
- **And** `NewSQLiteStore` returns `ErrSchemaVersionIncompatible` with error string containing `detected=2`, `expected=4`, and `migrate-cache`
- **Product gap (gap-012-v2-readonly-blocked)**: `NewSQLiteReadOnlyStore` gates on `PermitWrites` instead of `Compatible`. A read-only store opened against a version-2 cache returns `ErrSchemaVersionIncompatible` with a `SchemaVersionError` even though `Compatible==true`. This prevents diagnostic tools (doctor) from reading version information from caches that need migration. The acceptance criteria requires the compatibility diagnostic to be "reported" — but the read-only path blocks all access including reads. The doctor command cannot report schema version for version-2 caches.
- **Product gap (gap-012-v2-migrate-fragile)**: `MigrateCache` accepts version-2 caches for migration without validating that the initial schema tables exist. On a corrupted version-2 cache (schema_version=2 but missing tables), migrations fail with SQL errors rather than a graceful diagnostic. This is a synthetic scenario — a real iter-2 cache always reaches v2 through the full migration sequence and has all tables present.

## 012-internal-cache-task-1-add-schema-version-detection-and-compatibility-che-scenario-2

Iter-1 cache → incompatibility reported, re-initialization recommended.

- **Given** a cache database created by a pre-schema-versioning binary (no `schema_version` table, but contains user tables like `sources`)
- **When** `CheckVersionCompatibility` is called against the database
- **Then** `Compatible` is `false`
- **And** `PermitWrites` is `false`
- **And** `Message` indicates "pre-schema-versioning" or "iteration 1 equivalent"
- **And** `Remediation` contains a re-initialization recommendation (e.g., `reinit-cache` or "delete the cache file and re-sync")
- **And** `NewSQLiteStore` returns `ErrSchemaVersionIncompatible` with re-initialization message
- **And** `MigrateCache` returns no error, `Compatibility.Compatible=false`, and no migrations applied
- **Product gap (gap-012-v1-readonly-blocked)**: `NewSQLiteReadOnlyStore` also blocks on `PermitWrites=false` for iter-1 caches. While iter-1 caches are truly incompatible (no migration path), the doctor should still be able to report the incompatibility diagnostic rather than silently showing "not_available".

## 012-internal-cache-task-1-add-schema-version-detection-and-compatibility-che-scenario-3

Future version cache → binary downgrade not supported.

- **Given** a cache database with `schema_version.version` set higher than `currentSchemaVersion` (e.g., `5` when current is `4`)
- **When** `CheckVersionCompatibility` is called against the database
- **Then** `Compatible` is `false`
- **And** `PermitWrites` is `false`
- **And** `Message` indicates the schema version is newer than the supported version
- **And** `Remediation` recommends upgrading the binary
- **And** `NewSQLiteStore` called on this database returns `ErrSchemaVersionIncompatible` wrapped in a `SchemaVersionError` with an error string containing "newer", "detected=5", "expected=4", and "upgrade" or "binary"

## Product Gaps Summary

| Gap ID | Severity | Scenario | Description |
|--------|----------|----------|-------------|
| gap-012-v2-readonly-blocked | high | scenario-1 | `NewSQLiteReadOnlyStore` blocks on `PermitWrites=false` for version-2 caches even though `Compatible==true`. Read-only consumers (doctor, diagnostics) cannot access version-2 caches to report schema version information. Fix: gate read-only stores on `Compatible` first, then separately check `PermitWrites` only for read-write stores. |
| gap-012-v1-readonly-blocked | medium | scenario-2 | `NewSQLiteReadOnlyStore` blocks on `PermitWrites=false` for iter-1 caches. Doctor cannot report the incompatibility diagnostic to the operator. |
| gap-012-v2-migrate-fragile | low | scenario-1 | `MigrateCache` does not validate that schema tables exist before running downstream migrations on a version-2 cache. On corrupted caches with only `schema_version` set, migrations fail with SQL errors rather than a graceful diagnostic. This is a synthetic scenario (real iter-2 caches always have all tables). |
