# Validation Scenarios: Add migrate-cache CLI command

## Scenario Inventory

### 005-cmd-gitcode-mcp-task-5-add-migrate-cache-cli-command-scenario-1
- **Description**: `gitcode-mcp migrate-cache` against a version 2 cache upgrades the schema in place and preserves data.
- **Actor**: Operator running the current `gitcode-mcp` binary in offline default validation mode.
- **Expected Outcome**: The command exits 0, reports `from_version: 2`, `to_version: 4`, `status: migrated`, and existing repository data remains present after migration.
- **Evidence Type**: CLI command execution against a locally built binary with a temporary SQLite cache.

### 005-cmd-gitcode-mcp-task-5-add-migrate-cache-cli-command-scenario-2
- **Description**: `gitcode-mcp migrate-cache` against an iteration-1/version-1 cache reports incompatibility.
- **Actor**: Operator running the current `gitcode-mcp` binary in offline default validation mode.
- **Expected Outcome**: The command exits non-zero and reports `status: incompatible` plus remediation recommending cache re-initialization.
- **Evidence Type**: CLI command execution against a locally built binary with a temporary SQLite cache.

### 005-cmd-gitcode-mcp-task-5-add-migrate-cache-cli-command-scenario-3
- **Description**: `gitcode-mcp migrate-cache` against a current cache reports no migration needed.
- **Actor**: Operator running the current `gitcode-mcp` binary in offline default validation mode.
- **Expected Outcome**: The command exits 0 and reports `status: up_to_date`.
- **Evidence Type**: CLI command execution against a locally built binary with a temporary SQLite cache.

## Decommission Verification

### DECOMM-005-001: decommission-10
- **Target**: Silently opening old cache databases without schema version check or migration diagnostic.
- **Verification**: `gitcode-mcp migrate-cache --format json` must emit explicit detected/target schema versions and either migrate compatible version-2 caches or report actionable incompatibility for version-1 caches.
