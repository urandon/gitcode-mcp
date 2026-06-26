# Validation Scenarios: Change index_repo service delegation

## 009-internal-service-task-2-change-index_repo-service-delegation-internal-ser-scenario-1

SCN-MCP-INDEX-REPO-DELEGATES-SERVICE-INDEX: `TestMCPIndexRepoDelegatesServiceIndex` uses an `indexRepoSpyService` fake that records calls to both `Service.Index` and `Service.StaleIndex`. It invokes `tools/call` with `name=index_repo` and `repo_id: "fixture-a"`, then asserts `Index` call count is 1 and `StaleIndex` call count is 0. The structured response carries `Command: "index"`, `Status: "ok"`, and `ProcessedCount: 3`.

## 009-internal-service-task-2-change-index_repo-service-delegation-internal-ser-scenario-2

SCN-MCP-INDEX-REPO-NOT-STALE-DIAGNOSTIC: `TestMCPIndexRepoNotStaleDiagnostic` seeds a populated cache with two source records (`ISSUE-1`, `ISSUE-2`) under repo `index-repo-target`, invokes `tools/call` with `name=index_repo` and `repo_id: "index-repo-target"`, and asserts the structured response is an `OperationResult` with `Command: "index"` and `Status: "ok"` and `ProcessedCount > 0`. It also marshals the result and confirms no stale-index fields (`stale_count`, `affected_source_ids`, `missing_target_ids`) are present in the JSON.

## 009-internal-service-task-2-change-index_repo-service-delegation-internal-ser-scenario-3

SCN-MCP-INDEX-REPO-STALE-INDEX-NOT-CALLED: The `TestMCPIndexRepoDelegatesServiceIndex` spy test also validates this scenario by asserting `StaleIndex` call count is 0 from the MCP lifecycle `index_repo` tool path. The separate `TestMCPIndexRepoNotStaleDiagnostic` further confirms the `index_repo` response payload does not contain stale-index diagnostic fields.

## Offline validation command

Run:

```sh
bash tests/design_package/009-internal-service-task-2-change-index_repo-service-delegation-internal-ser/run.sh
```
