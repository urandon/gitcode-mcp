# Validation Scenarios: Add Live write commands (create-issue, create-comment, create-wiki-page, add-label)

## Scenario Inventory

### SCN-001: 003-cmd-gitcode-mcp-task-3-add-live-write-commands-create-issue-create-comm-scenario-1
- **Description**: gitcode-mcp create-issue --live --idempotency-key 'ik-001' --title 'Test' → issue created with cached record.
- **Actor**: Operator with GITCODE_TOKEN set, live repo bound.
- **Expected Outcome**: The `create-issue` command dispatches through `executeWrite` with `WriteModeLive`, calls the live gitcode client adapter, returns a `WriteCommandResult` with `Status: "succeeded"`, `RemoteID` set, and the remote ID recorded in the cache. The audit trail contains one entry for the write. Repeating the same `--idempotency-key` replays the result without a duplicate remote call.
- **Evidence Type**: Go test assertions (`TestWriteLiveSuccessAuditCacheAndReplay`) + CLI dispatch path verification.
- **Test Coverage**: `internal/service/service_test.go` — `TestWriteLiveSuccessAuditCacheAndReplay` exercises create-issue live write, audit trail recording, cache record persistence, and idempotency replay.

### SCN-002: 003-cmd-gitcode-mcp-task-3-add-live-write-commands-create-issue-create-comm-scenario-2
- **Description**: Repeat same command → 'already applied'. --dry-run → validates without remote call.
- **Actor**: Operator re-issuing the same create-issue command with the same `--idempotency-key`, or using `--dry-run`.
- **Expected Outcome**:
  - Replay: `create-issue --live --idempotency-key 'ik-001' --title 'Test'` when `ik-001` already has a `"succeeded"` audit entry → returns `WriteCommandResult` with `Replayed: true` and `Status: "succeeded"` without incrementing the client call count.
  - Dry-run: `create-issue --dry-run --title 'Test'` → returns `Status: "dry_run_valid"`, no remote call, no audit row recorded.
  - Add-label dry-run: `add-label --dry-run --number 1 --label "bug"` → returns `Status: "dry_run_valid"`, `Command: "add-label"`, no remote call.
- **Evidence Type**: Go test assertions.
- **Test Coverage**:
  - `internal/service/service_test.go` — `TestWriteLiveSuccessAuditCacheAndReplay` replay subtest; `TestWriteDryRunNoMutation` for create-issue dry-run; `TestAddLabelDryRunNoMutation` for add-label dry-run.
  - `internal/service/service_test.go` — `TestWritePartialCacheRefreshRetryUsesAuditWithoutSecondAdapterCall` exercises retry-replay after partial cache refresh failure.

### SCN-003: 003-cmd-gitcode-mcp-task-3-add-live-write-commands-create-issue-create-comm-scenario-3
- **Description**: Conflict scenario → conflict detected and reported.
- **Actor**: Operator issuing a write with an idempotency key that already exists but with a different payload (different title/body/label).
- **Expected Outcome**: `create-issue --live --idempotency-key 'same-key' --title 'Different'` when `same-key` was previously used with `--title 'Original'` → returns `ErrWriteFailure` with `Code: "write_idempotency_conflict"`. The remote adapter is not called again.
- **Evidence Type**: Go test assertions.
- **Test Coverage**: `internal/service/service_test.go` — `TestWriteIdempotencyConflictDetection` creates first issue with `same-key`, then attempts a second with different title → asserts `write_idempotency_conflict` error and verifies client was called only once.

## Decommission Verification

### DECOMM-005: decommission-5
- **Target**: create-issue --live returning 'fixture client is read-only' error.
- **Verification**:
  1. `Service.AddLabel` no longer returns `ErrWriteFailure{Code: "write_unsupported_deferred"}` — it calls `executeWrite` which dispatches to the live client adapter. Verified at `internal/service/service.go:876-881`.
  2. `callWriteAdapter` includes `case "add-label"` dispatching to `s.client.AddLabel` (line 1514-1520).
  3. `replayWriteGraph` includes `case "create-issue", "update-issue", "add-label"` handling (line 1543).
  4. `TestAddLabelLiveSuccessAuditCacheAndReplay` proves add-label works with live token.
  5. The old `write_unsupported_deferred` stub is gone — only the `default` case in `callWriteAdapter` and `replayWriteGraph` retains it for genuinely unsupported commands.
  6. `go test ./internal/service/...` passes with all write tests including `TestAddLabelLiveSuccessAuditCacheAndReplay` and `TestAddLabelDryRunNoMutation`.

## Implementation Coverage Summary

The task's core write infrastructure exists across all layers:

| Layer | Surface | Status |
|---|---|---|
| CLI dispatch | `internal/cli/cli.go:667-678` — create-issue, update-issue, create-page, update-page, add-comment, add-label dispatch through `dispatchWrite` → `handler(ctx, writeRequest(opts))` | Complete |
| CLI flags | `internal/cli/cli.go:695-706` — `validateWriteOptions` enforces `--repo`, rejects `--owner/--name/--api-base-url`, requires exactly one of `--dry-run` or `--live` | Complete |
| CLI write request | `internal/cli/cli.go:726-739` — `writeRequest` maps CLI options to `service.WriteCommandRequest` with Mode, Label, Labels, IdempotencyKey | Complete |
| Service write methods | `internal/service/service.go:839-881` — CreateIssue, UpdateIssue, CreatePage, UpdatePage, AddComment, AddLabel all call `executeWrite` | Complete |
| executeWrite orchestration | `internal/service/service.go:1403-1478` — builds adapter route, validates mode, generates idempotency, dry-run short-circuit, credential check, audit trail replay (idempotency/conflict detection), adapter call, cache refresh, audit recording | Complete |
| callWriteAdapter dispatch | `internal/service/service.go:1490-1538` — switch over command dispatching to `s.client.{CreateIssue,UpdateIssue,CreateIssueComment,AddLabel,CreateWikiPage,UpdateWikiPage}` | Complete |
| replayWriteGraph | `internal/service/service.go:1540-1575` — reconstructs cache records from audit trail entries for replay after partial failure | Complete |
| issueWriteGraph | `internal/service/service.go:1577-1583` — constructs record graph from issue write result | Complete |
| Live client interface | `internal/gitcode/client.go:17-23` — CreateIssue, UpdateIssue, CreateIssueComment, CreateWikiPage, UpdateWikiPage, AddLabel, RemoveLabel | Complete |
| Fake test client | `internal/service/service_test.go:1198-1249` — `fakeGitCodeClient` implements all write methods with call counting and result injection | Complete |
