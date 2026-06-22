# Validation Scenarios: 018 internal-provider-live live write idempotency gate and conflict detection

## Scope

These scenarios validate offline, deterministic evidence for live write behavior through production Go test paths that use local fake HTTP servers or fake clients only for the external GitCode dependency.

## Scenarios

### 018-internal-provider-live-task-3-add-live-write-idempotency-gate-and-conflict-dete-scenario-1

`create-issue --live` with an idempotency key checks the persisted audit trail before any API call.

Executable evidence:

- `TestWriteLiveSuccessAuditCacheAndReplay` performs a confirmed live-mode issue create through the service path, records an audit row, then repeats the same request and asserts replay/already-applied behavior without a second client call.
- `TestWritePartialCacheRefreshRetryUsesAuditWithoutSecondAdapterCall` proves a confirmed remote mutation is replayed from audit on retry without a second adapter call even when cache refresh previously failed.

Expected result: duplicate/retry execution returns a replayed result and the fake live client request count remains `1`.

### 018-internal-provider-live-task-3-add-live-write-idempotency-gate-and-conflict-dete-scenario-2

Duplicate idempotency key reports an already-applied replay, and a `409` response is mapped to conflict detection/reporting.

Executable evidence:

- `TestWriteLiveSuccessAuditCacheAndReplay` covers duplicate key replay/already-applied semantics with no duplicate remote call.
- `TestWriteIdempotencyConflictDetection` covers same-key/different-payload detection before a remote call and returns `write_idempotency_conflict`.
- `TestS018LiveWriteConflictMaps409` uses a local HTTP server returning `409` through the live-mode provider path and asserts `write_conflict` is reported.
- `TestFailureModes/conflict` and `TestWriteIdempotency/conflict returns local and remote payloads` validate the production HTTP client maps `409` responses to typed conflict evidence.

Expected result: duplicate key is replayed, payload mismatch fails before another remote call, and HTTP `409` returns a conflict error instead of succeeding or surfacing a generic error.

### 018-internal-provider-live-task-3-add-live-write-idempotency-gate-and-conflict-dete-scenario-3

Dry-run validates input without making an API call.

Executable evidence:

- `TestWriteDryRunNoMutation` runs create-issue in dry-run mode through the service write path and asserts `dry_run_valid`, generated idempotency metadata, zero client calls, and no audit rows.

Expected result: dry-run succeeds for valid inputs and leaves the fake live client/audit trail untouched.

## Decommission Coverage

`decommission-5` is validated by `TestS018LiveWriteUsesHTTPProviderAndRefreshesCache`: live-mode `create-issue` with a token uses a local HTTP-backed live provider, refreshes cache after remote confirmation, and does not return the fixture-only `fixture client is read-only` path.
