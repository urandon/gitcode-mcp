# Validation Scenarios: Add ZeroDelta to SyncResult with persistent event

Task: 016-internal-sync-task-3-add-zerodelta-to-syncresult-with-persistent-event

## Scenario: 016-internal-sync-task-3-add-zerodelta-to-syncresult-with-persistent-event-scenario-1

**Description:** A Go test `go test -run TestZeroDeltaPersistentEvent -count=1 ./internal/service/` calls `SyncToCache` twice with unchanging fixture data; the second call returns `SyncResult.ZeroDelta == true`, `SyncResult.Counts.Fetched > 0`, `SyncResult.Counts.Skipped == SyncResult.Counts.Fetched`; a store query verifies two `SyncEvent` rows exist for that source (both in-progress and succeeded persisted), the second succeeded event has `ZeroDelta == true`, and count fields are preserved.

**Exercise:** Run the named test against the production service package.

**Expected:** Test passes. First sync `ZeroDelta=false`, second sync `ZeroDelta=true`, `Skipped==Fetched>0`, `Updated==0`, `Inserted==0`, two succeeded events persisted for ISSUE-42, stored event `ZeroDelta=true`, count fields preserved via JSON round-trip, replayed event carries `ZeroDelta`, `SyncStatus` summary `ZeroDelta=true`.

## Scenario: 016-internal-sync-task-3-add-zerodelta-to-syncresult-with-persistent-event-scenario-2

**Description:** A separate test with content that actually changed between syncs verifies `ZeroDelta == false`.

**Exercise:** Run the named test.

**Expected:** `TestZeroDeltaFalseWhenContentChanges` passes. Content changed produces `ZeroDelta=false`, `Updated=1`.

## Scenario: 016-internal-sync-task-3-add-zerodelta-to-syncresult-with-persistent-event-scenario-3

**Description:** The `sync_status` summary for the zero-delta source reflects `ZeroDelta: true` from `SyncStatusSummaryResult`.

**Exercise:** Run `TestZeroDeltaPersistentEvent` which calls `SyncStatus` after a zero-delta sync.

**Expected:** `summary.ZeroDelta == true`.
