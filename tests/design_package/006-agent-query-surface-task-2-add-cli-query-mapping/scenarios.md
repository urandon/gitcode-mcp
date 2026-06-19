# Scenarios: Add CLI Query Mapping

## 006-agent-query-surface-task-2-add-cli-query-mapping-scenario-1

A developer runs `go test ./internal/cli/... -run TestMinimumReplacementBar`; the trigger is a cold test cache populated through the real `gitcode-mcp ingest` command, the target product surface is the CLI query route, and the expected outcome is `gitcode-mcp search_sources "backlog"`, `gitcode-mcp list_sources --kind task --status ready`, `gitcode-mcp get_source DOC-123`, `gitcode-mcp source_backlinks DOC-123`, and `gitcode-mcp sync_status DOC-123` returning expected offline results without shell or network access.

Executable evidence: `run.sh` invokes `go test ./internal/cli/... -run TestMinimumReplacementBar -count=1`. The validation is intentionally bound to the production CLI query route and fails if the scenario is not implemented as executable CLI behavior.

## 006-agent-query-surface-task-2-add-cli-query-mapping-scenario-2

A developer runs `go test ./internal/cli/... -run 'Test(SearchSourcesJSON|RecentJSON|LinkCheckJSON|StaleIndexJSON)'`; the trigger is cache-backed CLI execution, and the expected visible response is valid JSON containing the service DTO fields for search results, recent changes, link-check findings, and stale-index findings.

Executable evidence: `run.sh` invokes `go test ./internal/cli/... -run 'Test(SearchSourcesJSON|RecentJSON|LinkCheckJSON|StaleIndexJSON)' -count=1`. The tests parse CLI stdout as JSON and assert DTO fields for the cache-backed query/inspection commands.

## 006-agent-query-surface-task-2-add-cli-query-mapping-scenario-3

A developer runs `go test ./internal/cli/... -run TestHelpDocumentsShellMapping`; the trigger is `gitcode-mcp --help`, and the expected visible output documents the shell-equivalent mapping plus `recent`, `link-check`, and `stale-index` inspection commands.

Executable evidence: `run.sh` invokes `go test ./internal/cli/... -run TestHelpDocumentsShellMapping -count=1`. The test exercises CLI help output rather than static documentation inventory.

## 006-agent-query-surface-task-2-add-cli-query-mapping-scenario-4

A developer runs `go test ./internal/cli/... -run TestQueryCommandErrors`; the trigger is CLI execution for empty cache, not found id, invalid snippet range, clamped snippet range, stale-index strict mode, and link-check strict mode, and the expected outcome is the specified stdout/stderr/exit-code behavior through the real CLI route.

Executable evidence: `run.sh` invokes `go test ./internal/cli/... -run TestQueryCommandErrors -count=1`. The test validates typed service error translation into CLI-visible stdout, stderr, and exit codes.

## 006-agent-query-surface-task-2-add-cli-query-mapping-scenario-5

A developer runs `go test ./internal/cli/... -run TestQueryCommandsUseServiceOnly`; the trigger is CLI query execution with a test service factory, and the expected outcome is service methods are called while no raw shell command, direct markdown read path, index-build path, or live network path is used.

Executable evidence: `run.sh` invokes `go test ./internal/cli/... -run TestQueryCommandsUseServiceOnly -count=1`. The test uses the CLI service-factory seam to prove dispatch reaches service methods for each query command without requiring shell or network behavior.
