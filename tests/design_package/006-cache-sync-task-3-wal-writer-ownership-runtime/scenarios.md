# Validation Scenarios: 006-cache-sync-task-3-wal-writer-ownership-runtime

## 006-cache-sync-task-3-wal-writer-ownership-runtime-scenario-1

A system test triggers the cache runtime path by opening one temporary SQLite cache, starts two reader clients through the service/MCP-equivalent read path, then holds a sync/index writer lease and attempts a second writer plus a migration; reader queries continue where SQLite permits, the second writer receives a typed busy/owned error including operation and start time, and migration is blocked until the lease is released.

Concrete offline validation:

1. Run the production cache runtime test `TestWriterAdmissionWALOwnershipRuntime` in `internal/cache`.
2. The test opens a file-backed temporary SQLite database with `NewSQLiteStore`, which exercises production initialization, WAL setup, busy timeout, and migration lease acquisition.
3. The test seeds repo-scoped cache data using production store APIs.
4. The test opens two independent reader store instances and verifies cache-first reader queries continue while a `sync-index` writer lease is held.
5. The test attempts a conflicting writer and requires `ErrLockContention` with active owner metadata including operation, non-zero start time, and process id.
6. The test attempts another production `NewSQLiteStore` open while the writer lease is held and requires migration/open to be blocked with the same typed contention class.
7. The test releases the lease and verifies writer acquisition and migration/open succeed afterward.

## 006-cache-sync-task-3-wal-writer-ownership-runtime-scenario-2

Executable evidence is Go concurrency/runtime tests using production cache locking code and a temporary SQLite database.

Concrete offline validation:

1. Run `go test ./internal/cache -run 'TestWriterAdmissionWALOwnershipRuntime|TestCheckpointAfterWriteHeavySync|TestLockContention' -count=1` with live validation disabled.
2. Run `go test ./internal/service -run 'TestSyncLockContention' -count=1` to verify the service sync product path uses production writer-lock contention handling.
3. Run `go test ./...` to ensure the runtime/compiler test evidence is current across the repository.
4. Run `git diff --check` to reject whitespace errors in the validation materialization.
