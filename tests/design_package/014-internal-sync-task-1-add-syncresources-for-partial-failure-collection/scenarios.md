# Scenarios: Add SyncResources for partial-failure collection

## 014-internal-sync-task-1-add-syncresources-for-partial-failure-collection-scenario-1

**Description:** Partial-failure test — one source succeeds, one fails with `ErrNotFound`.

**Steps:**
1. Construct `*Service` via `NewWithClient` with a mock `gitcode.Client`.
2. Configure the mock so `GetIssue` succeeds (returns valid `gitcode.Issue` with comments) and `GetWikiPage` fails with `gitcode.ErrNotFound`.
3. Call `svc.SyncResources(ctx, reqs)` with two `SyncRequest` values (issue and wiki).
4. Assert `SyncResourcesResult.SuccessCount == 1`.
5. Assert `SyncResourcesResult.FailureCount == 1`.
6. Assert `Results[0].Counts.Fetched > 0`.
7. Assert `Failures[0].SourceID` matches the failed source.
8. Assert a `*PartialSyncError` is returned via `errors.As`.
9. Assert successful resource records are committed to cache via `store.GetSourceScoped`.

**Expected result:** All assertions pass.

**Validation notes:** The `RemoteType` field on `ResourceError` depends on whether the caller sets `AliasType` on `SyncRequest`. When `AliasType` is not set, `RemoteType` is empty — this is by design, not a bug. The acceptance criteria do not require a specific `RemoteType` value.

## 014-internal-sync-task-1-add-syncresources-for-partial-failure-collection-scenario-2

**Description:** All-successful test — all requests succeed.

**Steps:**
1. Construct `*Service` via `NewWithClient` with a mock `gitcode.Client`.
2. Configure the mock so both `GetIssue` and `GetWikiPage` succeed (return valid data).
3. Call `svc.SyncResources(ctx, reqs)` with two `SyncRequest` values (issue and wiki).
4. Assert `PartialSyncError` is nil (no error returned from `SyncResources`).
5. Assert `SyncResourcesResult.SuccessCount == len(reqs)`.
6. Assert `SyncResourcesResult.FailureCount == 0`.
7. Assert both resource records are committed to cache via `store.GetSourceScoped`.

**Expected result:** All assertions pass.
