# Validation Scenarios

Task: 007-internal-mcp-task-7-validate-mcp-startup-readiness-diagnostic-tests-i
Title: Validate MCP startup/readiness diagnostic tests (i

## Scenario Inventory

### 007-internal-mcp-task-7-validate-mcp-startup-readiness-diagnostic-tests-i-scenario-1: go test ./internal/mcp/... passes.

Verifies that the MCP test suite passes, confirming all startup diagnostic test functions compile and execute correctly.

**Product surface**: `internal/mcp/` Go tests
**Evidence type**: unit test
**Mock policy**: internal-only (no external dependencies)

**Verification**: Run `go test ./internal/mcp/...`; exit code must be 0.

### 007-internal-mcp-task-7-validate-mcp-startup-readiness-diagnostic-tests-i-scenario-2: Each failure scenario produces typed diagnostic in tools/list capability metadata.

Verifies that for each startup failure class (cache_path_unwritable, schema_incompatible, cache_lock_contention, startup-failure), the `tools/list` JSON-RPC response includes a `startup_diagnostic` field in the result with the correct error_class, non-empty message, and non-empty remediation text. Also verifies that only the `doctor` tool is advertised in `tools/list` for minimal-server (failure-mode) paths.

**Product surface**: MCP `tools/list` endpoint via `RPCHandler.Handle()` (internal/mcp/mcp_test.go)
**Evidence type**: unit test (Go test)
**Mock policy**: internal-only (no external dependencies; uses `NewMinimalRPCHandler` with constructed `StartupDiagnostic` values)

**Verification**: For each diagnostic class:
- `tools/list` result contains `startup_diagnostic` with `error_class` matching expected class
- `startup_diagnostic.message` is non-empty
- `startup_diagnostic.remediation` is non-empty and contains expected keyword (e.g., "upgrade" for schema_incompatible, "retry" for cache_lock_contention, "chmod" for cache_path_unwritable)
- `tools/list` result lists exactly one tool: `doctor`
- For startup-failure, message does not leak raw error details (stack traces, file paths, panic messages)

### 007-internal-mcp-task-7-validate-mcp-startup-readiness-diagnostic-tests-i-scenario-3: Doctor tool call returns structured diagnostic body with actionable remediation text for each failure class.

Verifies that for each startup failure class, calling the `doctor` MCP tool returns a structured result with status `"degraded"`, exactly one diagnostic entry, and that entry contains the correct `error_class`, non-empty `message`, and actionable `remediation` text referencing concrete recovery steps (CLI commands or UI steps, not raw stack traces).

**Product surface**: MCP `doctor` tool via `RPCHandler.Handle()` (internal/mcp/mcp_test.go)
**Evidence type**: unit test (Go test)
**Mock policy**: internal-only (no external dependencies; uses `NewMinimalRPCHandler` with constructed `StartupDiagnostic` values)

**Verification**: For each diagnostic class:
- `doctor` result status is `"degraded"`
- `doctor` result diagnostics array has exactly 1 entry
- Diagnostic entry has `error_class` matching expected class
- Diagnostic entry has non-empty `message`
- Diagnostic entry has non-empty `remediation` containing expected keyword
- For startup-failure, message does not leak raw error details

## Detailed Scenario Coverage

| Test Function | Scenarios Covered | Diagnostic Classes |
|---|---|---|
| TestStartupDiagnosticInjection | SCN-2, SCN-3 | cache_path_unwritable |
| TestStartupDiagnosticSchemaIncompatible | SCN-2, SCN-3 | schema_incompatible |
| TestStartupDiagnosticCacheLockContention | SCN-2, SCN-3 | cache_lock_contention |
| TestStartupDiagnosticStartupFailure | SCN-2, SCN-3 | startup-failure |
| TestStartupDiagnosticAllScenarios | SCN-2, SCN-3 | all 4 classes (table-driven) |
| TestStartupDiagnosticRemediationText | SCN-2 | schema_incompatible + startup-failure (factory-level) |
