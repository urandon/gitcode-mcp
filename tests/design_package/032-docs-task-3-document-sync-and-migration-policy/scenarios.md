# Validation Scenarios: 032-docs-task-3-document-sync-and-migration-policy

## 032-docs-task-3-document-sync-and-migration-policy-scenario-1

AC-1 compatibility matrix is validated against the implemented cache-open behavior. The documentation must identify the current schema version from the product implementation, describe version 1 / pre-versioned caches as blocked, and the runtime must reject a version-1-equivalent non-empty SQLite cache with a non-zero exit and schema incompatibility diagnostic that includes the detected and expected versions.

## 032-docs-task-3-document-sync-and-migration-policy-scenario-2

The local command evidence for AC-1 is materialized by creating an offline temporary SQLite cache with `schema_version(version)=1`, running a read command against that cache, and checking stderr plus exit code for the version mismatch diagnostic.

## 032-docs-task-3-document-sync-and-migration-policy-scenario-3

AC-2 migration help is validated against the documented migration path. The documentation must describe migration from supported older versions to the implemented current schema, backup creation, and the required `--confirm` gate.

## 032-docs-task-3-document-sync-and-migration-policy-scenario-4

The local command evidence for AC-2 is materialized by running `gitcode-mcp migrate-cache --help` through `go run ./cmd/gitcode-mcp migrate-cache --help` and comparing the help output to the documented migration policy.

## 032-docs-task-3-document-sync-and-migration-policy-scenario-5

AC-3 sync status help and model semantics are validated together. The documentation must describe sync event timestamps and delta / zero-delta reporting while remaining consistent with the implemented `sync_status --help` surface.

## 032-docs-task-3-document-sync-and-migration-policy-scenario-6

The local command evidence for AC-3 is materialized by running `gitcode-mcp sync_status --help` through `go run ./cmd/gitcode-mcp sync_status --help` and verifying the help surface remains available while the documentation covers persisted timestamp and delta semantics.
