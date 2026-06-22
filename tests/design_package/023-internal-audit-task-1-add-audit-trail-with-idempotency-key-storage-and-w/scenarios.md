# Validation scenarios for task 023

## 023-internal-audit-task-1-add-audit-trail-with-idempotency-key-storage-and-w-scenario-1

`create-issue --live` with an idempotency key is exercised through the service live write path with a stubbed GitCode client. The scenario validates that the key is stored in the audit trail with a `succeeded` outcome, operation `create-issue`, record id, remote type/id, and a non-empty payload hash.

## 023-internal-audit-task-1-add-audit-trail-with-idempotency-key-storage-and-w-scenario-2

Repeating the same `create-issue --live` request with the same idempotency key is exercised through the service live write path. The scenario validates that the result is replayed/already-applied, the adapter call count remains one, and no duplicate audit row is created.

## 023-internal-audit-task-1-add-audit-trail-with-idempotency-key-storage-and-w-scenario-3

A prior failed outcome for the same key is exercised through the service live write path by first returning a stubbed network failure and then retrying the same request. The scenario validates that the failure is recorded with a retry-safe audit row and that retry is allowed and succeeds without being blocked by the prior failure.

## Additional task-plan scenario guards

- `S023-audit-success-replay`: success audit storage and same-key replay are exercised without a second adapter call.
- `S023-audit-conflict`: the same repo/key with a different payload returns `write_idempotency_conflict` and does not call the adapter again.
- `S023-audit-failure-retry`: a failed audit outcome is stored and the same key/payload can be retried successfully.
- `S023-audit-partial-refresh`: a remote-confirmed/cache-refresh-failed audit entry is replayed through cache refresh without a duplicate remote write.
- `S023-audit-repo-scope`: the same idempotency key can be applied independently in two repositories.

The validation also checks that a repeated successful idempotency key does not perform a second adapter call, replacing the prior duplicate-live-write/read-only-fixture failure surface for explicit live writes.
