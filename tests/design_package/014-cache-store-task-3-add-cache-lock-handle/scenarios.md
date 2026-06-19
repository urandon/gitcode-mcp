# Validation Scenarios

## 014-cache-store-task-3-add-cache-lock-handle-scenario-1
A developer triggers `go test ./internal/cache/... -run 'TestLockContention|TestLockContentionBlocksSimulatedSync'`; the cache lock API acquires a lock file, attempts a second acquisition, receives typed `ErrLockContention` with the lock path, releases the first handle, and then successfully acquires again.

Executable validation: `TestLockContention` must pass as a cache package product-path test. It must call the real `Store.AcquireLock` and `Store.ReleaseLock` implementation against a local lock file, assert `errors.As` compatibility with `ErrLockContention`, assert the returned error path equals the lock path, verify double release and nil release are safe, and verify reacquisition succeeds after release.

## 014-cache-store-task-3-add-cache-lock-handle-scenario-2
The simulated sync test holds the lock, attempts the service-style lock-before-mutate path, verifies `UpsertSourceGraph` is not called while `ErrLockContention` is active, and confirms through `GetSyncStatus` and source row counts that no partial data was written.

Executable validation: `TestLockContentionBlocksSimulatedSync` must pass as a cache package product-path test. It must hold the real lock, run a local lock-before-mutate helper that would call `UpsertSourceGraph` only after successful lock acquisition, receive `ErrLockContention`, prove the mutation callback was not called, verify `ListSources` returns no source rows, and verify `GetSyncStatus` for the would-be source returns `ErrNotFound`.

## 014-cache-store-task-3-add-cache-lock-handle-scenario-3
The executable evidence is local cache tests only, with no network access.

Executable validation: the run script executes only local Go cache tests plus deterministic repository checks. It does not start live GitCode/API/device validation, does not require credentials, and fails non-zero if the cache lock tests or offline regression checks fail.
