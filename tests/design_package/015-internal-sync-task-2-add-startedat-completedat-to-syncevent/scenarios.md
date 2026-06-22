# Scenarios: Add StartedAt/CompletedAt to SyncEvent

## 015-internal-sync-task-2-add-startedat-completedat-to-syncevent-scenario-1

**Description:** Successful `SyncToCache` returns non-zero `StartedAt`/`CompletedAt` with `CompletedAt.After(StartedAt)`.

**Steps:**
1. Construct `*Service` via `NewWithClient` with a mock `gitcode.Client` and an in-memory SQLite store.
2. Bind a repository with issue scope.
3. Override `svc.now` with a sequence of monotonically increasing times.
4. Call `svc.SyncToCache(ctx, SyncRequest{RemoteAlias: "issue:42"})`.
5. Assert `SyncResult.StartedAt` is non-zero.
6. Assert `SyncResult.CompletedAt` is non-zero.
7. Assert `SyncResult.CompletedAt.After(SyncResult.StartedAt)`.
8. Assert the stored sync event retrieved via `GetSyncEventByKey` also has non-zero `StartedAt` and `CompletedAt`.

**Expected result:** All assertions pass.

## 015-internal-sync-task-2-add-startedat-completedat-to-syncevent-scenario-2

**Description:** Zero-`CompletedAt` events are excluded from `SyncStatusSummaryResult.LastSyncCompletedAt` selection.

**Steps:**
1. Create an in-memory SQLite store with a repo binding.
2. Insert a source graph for `ISSUE-42`.
3. Insert one incomplete `SyncEvent` row with `CompletedAt` zero (status `in_progress`).
4. Insert one completed `SyncEvent` row with `CompletedAt` non-zero (status `succeeded`).
5. Call `svc.SyncStatus(ctx, ListSourcesRequest{RepoID: "sync-status-completed"})`.
6. Assert `SyncStatusSummaryResult.LastSyncStartedAt` equals the completed event's `StartedAt`.
7. Assert `SyncStatusSummaryResult.LastSyncCompletedAt` equals the completed event's `CompletedAt`.
8. Assert `LastSyncCompletedAt` does NOT equal the incomplete event's `StartedAt` (confirming the incomplete event was excluded).

**Expected result:** All assertions pass.

## 015-internal-sync-task-2-add-startedat-completedat-to-syncevent-scenario-3

**Description:** `RecordSyncEvent` persists and reads back non-empty `started_at` and `completed_at`.

**Steps:**
1. Create an in-memory SQLite store with a source graph.
2. Insert a `SyncEvent` with explicit `StartedAt` and `CompletedAt` via `RecordSyncEvent`.
3. Read back via `GetSyncEventByKey`.
4. Assert `got.StartedAt` equals the inserted `StartedAt`.
5. Assert `got.CompletedAt` equals the inserted `CompletedAt`.

**Expected result:** All assertions pass.
