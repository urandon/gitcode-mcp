# Validation Scenarios: 020-internal-cache-task-1-change-cache-lock-strategy-and-wal-mode-internal

## 020-internal-cache-task-1-change-cache-lock-strategy-and-wal-mode-internal-scenario-1

Two concurrent read commands (search_sources) against same cache path: both complete without internal_error.

Concrete offline validation:

1. Run the production cache runtime test `TestWriterAdmissionWALOwnershipRuntime` in `internal/cache`.
2. The test opens a file-backed temporary SQLite database with `NewSQLiteStore`, which exercises WAL mode, `PRAGMA journal_mode = WAL`, `PRAGMA busy_timeout = 5000`, and production initialization.
3. The test opens two independent reader `*SQLiteStore` instances against the same on-disk database.
4. While a writer lease is held, both readers execute `GetSourceScoped` queries against the production cache.
5. The test asserts both reader queries complete without error, proving WAL-mode concurrent read isolation — no `internal_error` is returned for concurrent reads.

## 020-internal-cache-task-1-change-cache-lock-strategy-and-wal-mode-internal-scenario-2

Writer hold: concurrent readers complete.

Concrete offline validation:

1. The same `TestWriterAdmissionWALOwnershipRuntime` test covers this: lines 721-729 run two reader `GetSourceScoped` calls while a writer lease (`AcquireWriter` with `Operation: "sync-index"`) is held.
2. Both reader calls complete successfully, producing non-nil `Source` results.
3. This proves that exclusive writer lock does not block concurrent WAL readers.

## 020-internal-cache-task-1-change-cache-lock-strategy-and-wal-mode-internal-scenario-3

Two concurrent writers: one returns cache_busy not internal_error.

Concrete offline validation:

1. Run `TestCacheBusyDiagnosticCodeOnLockContention` in `internal/cache`.
2. The test acquires a writer lease via `store.AcquireWriter(ctx, WriterRequest{Operation: "sync"})`.
3. It then attempts a second `store.AcquireWriter` call on the same store.
4. Asserts the error is `*ErrLockContention` (not `nil`, not `internal_error`).
5. Asserts `contention.DiagnosticCode() == "cache_busy"` — the typed diagnostic, not the old opaque `internal_error` fallback.
6. Additionally runs `TestClassifierCacheBusy` in `internal/diagnostics` which proves:
   - `CodeCacheBusy` constant exists with value `"cache_busy"`.
   - Classification produces `CodeCacheBusy` not `CodeConfigurationError`.
   - `ExitClass` is `"cache"`, `Retryable` is `true`, `HTTPAttempted` is `false`.
   - `ErrLockContention` directly classifies as `CodeCacheBusy` via `DiagnosticCode()`.
7. Additionally runs `TestMCPRuntimeLockContentionErrorMapping` in `internal/mcp` which proves the MCP error-level code maps `ErrLockContention` to `"cache_busy"` (default case) or specific operation codes, and the error data carries operation/repo/pid metadata.

## 020-internal-cache-task-1-change-cache-lock-strategy-and-wal-mode-internal-scenario-4

Three goroutines (2 readers + 1 writer): readers complete while writer held; writer gets cache_busy diagnostic. go test ./internal/cache/... passes.

Concrete offline validation:

1. Run `TestThreeReadersOneWriterConcurrency` in `internal/cache`.
2. The test opens a file-backed SQLite database with WAL mode, seeds it with repo-scoped data.
3. Opens two independent reader stores against the same database.
4. Acquires a writer lease (`AcquireWriter` with `Operation: "sync-index"`).
5. While the writer lease is held, both readers execute `GetSourceScoped` — both complete without error, proving readers are not blocked.
6. A third goroutine attempts `readerOne.AcquireWriter` — must produce `ErrLockContention` with `DiagnosticCode() == "cache_busy"`.
7. Runs `go test ./internal/cache/...` which must return exit code 0.
