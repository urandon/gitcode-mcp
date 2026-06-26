# Validation Scenarios

Task: 022-internal-cache-task-3-validate-cache-concurrency-and-lock-diagnostics-te

## Scenario Inventory

- `022-internal-cache-task-3-validate-cache-concurrency-and-lock-diagnostics-te-scenario-1`: go test ./internal/cache/... passes.
- `022-internal-cache-task-3-validate-cache-concurrency-and-lock-diagnostics-te-scenario-2`: All concurrency scenarios verified with actual WAL-mode SQLite.
- `022-internal-cache-task-3-validate-cache-concurrency-and-lock-diagnostics-te-scenario-3`: Lock contention diagnostics are typed cache_busy.
- `022-internal-cache-task-3-validate-cache-concurrency-and-lock-diagnostics-te-scenario-4`: No internal_error for lock contention cases..

## Product Scenarios

### SCN-022-01: Two Concurrent SearchSources On WAL-mode Store

**Product behavior:** Two concurrent `SearchSources` calls against the same file-backed WAL-mode SQLite store both complete without any `internal_error`.

**Test:** `TestConcurrentSearchSources` in `internal/cache/concurrency_test.go:13`

**Verification:**
- Creates a file-backed WAL-mode store at a temp path
- Inserts 20 test sources under fixture-a
- Launches 2 concurrent goroutines, each calling `store.SearchSources(ctx, SearchQuery{Query: "Search"})`
- Collects errors via channel; asserts no error and non-zero results from each goroutine
- Passes `go test -count=1` and `go test -race -count=1`

---

### SCN-022-02: Writer Hold Does Not Block Concurrent Readers

**Product behavior:** When a writer holds the exclusive cache lock, concurrent readers operating via independent SQLite connections can still read, search, and list sources successfully.

**Test:** `TestWriterHoldReadersUnblocked` in `internal/cache/concurrency_test.go:51`

**Verification:**
- Creates a file-backed WAL-mode store and inserts a test source
- Opens two independent reader stores against the same file
- Acquires the exclusive writer lock on the primary store
- Each reader performs: `GetSourceScoped`, `SearchSources`, and `ListSources` while the writer is held
- All reader operations complete without error and return expected data
- Passes `go test -count=1` and `go test -race -count=1`

---

### SCN-022-03: Two Concurrent Writers — One Returns cache_busy

**Product behavior:** When two writers contend for the exclusive lock, the loser receives a typed `ErrLockContention` error whose `DiagnosticCode()` returns `"cache_busy"` — not `"internal_error"`.

**Test:** `TestTwoWritersContentionCacheBusy` in `internal/cache/concurrency_test.go:104`

**Verification:**
- Creates a file-backed WAL-mode store
- Acquires first writer lease (operation "sync")
- Attempts second writer lease (operation "write")
- Second attempt fails with `ErrLockContention`
- `contention.DiagnosticCode() == "cache_busy"`
- Contention metadata includes the holder's operation name, non-zero timestamp, and non-zero PID
- Passes `go test -count=1` and `go test -race -count=1`

---

### SCN-022-04: Three Goroutines (2 Readers + 1 Writer)

**Product behavior:** Three concurrent goroutines — two readers and one writer attempt — the readers complete successfully while the writer (attempting to acquire after the holder) receives `cache_busy`.

**Test:** `TestThreeRoutinesTwoReadersOneWriter` in `internal/cache/concurrency_test.go:142`

**Verification:**
- Creates a file-backed WAL-mode store and inserts a test source
- Opens two independent reader stores
- Acquires writer lock on the primary store
- Launches reader 1 (`GetSourceScoped`) and reader 2 (`SearchSources`) concurrently via `sync.WaitGroup`
- Both readers complete without error, returning expected data
- Attempts a second `AcquireWriter` — fails with `ErrLockContention`
- `contention.DiagnosticCode() == "cache_busy"`
- Passes `go test -count=1` and `go test -race -count=1`

---

### SCN-022-05: Future Schema Version Produces schema_incompatible

**Product behavior:** Opening a cache file whose `schema_version > currentSchemaVersion (11)` returns `ErrSchemaVersionIncompatible` wrapped in `*SchemaVersionError`.

**Test:** `TestFutureSchemaIncompatibleDiagnostic` in `internal/cache/concurrency_test.go:219`

**Verification:**
- Writes a file with `schema_version` set to `currentSchemaVersion + 1` (i.e., 12)
- `NewSQLiteStore` returns non-nil error
- Error unwraps to `ErrSchemaVersionIncompatible`
- Error is `*SchemaVersionError` type
- Passes `go test -count=1`
