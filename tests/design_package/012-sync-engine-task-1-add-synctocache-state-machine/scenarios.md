# Validation Scenarios

## 012-sync-engine-task-1-add-synctocache-state-machine-scenario-1
A developer triggers the service API by running `go test ./internal/service/... -run TestSync`; the test inserts a stale cache record, calls `GetSyncStatus`, and receives a stale result for the target stable id.

## 012-sync-engine-task-1-add-synctocache-state-machine-scenario-2
The same test runs `SyncToCache` against fixture-backed remote data, then observes updated source content, a fresh `GetSyncStatus` result, and a `sync_events` row containing the idempotency key, `succeeded` state, and reconciliation counts.

## 012-sync-engine-task-1-add-synctocache-state-machine-scenario-3
A concurrent-access test holds the cache lock and calls `SyncToCache` again through the same service surface; the second call returns typed `ErrLockContention`, performs no remote fetch, and leaves cache records unchanged.

## 012-sync-engine-task-1-add-synctocache-state-machine-scenario-4
A retry test runs `SyncToCache` against a fixture-backed adapter that returns retryable typed `ErrRateLimited` with `RetryAfter` and/or typed temporary timeout before success; the service re-attempts only at the sync-call level, then commits exactly once after a complete successful adapter result.

## 012-sync-engine-task-1-add-synctocache-state-machine-scenario-5
An idempotency replay test calls `SyncToCache` twice with the same idempotency key after a successful first run while an unrelated lock is held; the second call returns the prior result with `Replayed=true`, does not acquire the lock, creates no duplicate source/version/conflict mutations, and does not call the remote fixture server.

## 012-sync-engine-task-1-add-synctocache-state-machine-scenario-6
A bounded-staging test forces an error after partial remote data is received or after `MaxSize` is exceeded and verifies no partial source/version/conflict rows are visible.
