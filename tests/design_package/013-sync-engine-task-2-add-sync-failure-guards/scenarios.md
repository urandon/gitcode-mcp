# Validation Scenarios

## 013-sync-engine-task-2-add-sync-failure-guards-scenario-1
A developer triggers the failure suite by running `go test ./internal/service/... -run TestFailureModes`; each scenario calls the sync-engine `SyncToCache` product API and receives the expected typed error with prescribed visible message and recovery guidance.

Executable validation: `TestFailureModes` must pass as a product-path service test. It must exercise `SyncToCache` rather than only inspecting source text, and each subtest must use `errors.As`-compatible typed errors plus user-visible error messages and non-empty recovery guidance.

## 013-sync-engine-task-2-add-sync-failure-guards-scenario-2
The timeout scenario verifies retries are exhausted or context cancellation occurs, the target record remains unchanged, failed sync-event evidence is present, and the error includes the record id plus retry suggestion.

Executable validation: the `failure-timeout-network-unavailable` subtest must call `SyncToCache` for `DOC-123`, receive `ErrNetworkUnavailable` wrapped by service failure context, verify the visible message includes `DOC-123` and `retry with --timeout`, verify a failed sync event exists, and verify the cached source body/hash and remote revision remain unchanged.

## 013-sync-engine-task-2-add-sync-failure-guards-scenario-3
The rate-limit scenario verifies `RetryAfter` is present, `Retry-After` controls service-level delay when retrying is possible, and no partial page data is written after final failure.

Executable validation: the `failure-rate-limited-retry-after` subtest must call `SyncToCache`, receive `ErrRateLimited` with `RetryAfter`, verify the visible message includes the retry-after duration, verify failed sync-event evidence, and verify no source/version/conflict partial writes occurred. Supporting retry-delay behavior is validated by `TestSyncRetry`, which uses a retryable `ErrRateLimited` before success and proves service-level retry then a single successful reconciliation.

## 013-sync-engine-task-2-add-sync-failure-guards-scenario-4
Additional scenarios cover partial JSON, auth expiry, remote id collision, cache corruption, lock contention, missing remote record, and oversized payload with executable cache-state assertions after each run.

Executable validation: `TestFailureModes` must include subtests named `failure-partial-response`, `failure-auth-expired`, `failure-remote-id-collision`, `failure-cache-corruption`, `failure-lock-contention`, `failure-missing-remote-record`, and `failure-oversized-payload`. Each subtest must call `SyncToCache`, assert the expected typed error, assert the prescribed visible message when applicable, verify failed sync-event evidence except lock contention, verify lock contention performs no remote fetch and creates no event, verify missing remote only marks remote status as not found, and verify all other cases leave source/version/identity/conflict state unchanged.
