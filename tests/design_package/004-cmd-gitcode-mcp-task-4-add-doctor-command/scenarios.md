# Validation Scenarios: Add doctor command

## Scenario Inventory

### 004-cmd-gitcode-mcp-task-4-add-doctor-command-scenario-1
- **Description**: `gitcode-mcp doctor` reports version, config, cache, repo binding, token source (redacted), live provider reachable, auth probe, last sync, index freshness, MCP transport — all public-safe.
- **Actor**: Operator running the current `gitcode-mcp` binary in offline default validation mode.
- **Expected Outcome**: The command exits 0 and emits a readiness report with explicit sections or fields for version, config, cache, repo, credential/token source, live provider reachability, auth probe status, sync/last-sync, index freshness, and MCP transport. Output must not expose raw tokens, Authorization headers, cookies, or private/live endpoint material.
- **Evidence Type**: CLI command execution against a locally built binary with temporary config/cache.

### 004-cmd-gitcode-mcp-task-4-add-doctor-command-scenario-2
- **Description**: With no binding → 'no repo bound' + bind suggestion.
- **Actor**: Operator running `gitcode-mcp doctor` with an empty temporary cache and no repository binding.
- **Expected Outcome**: The report includes `no_repo_bound` or `no repo bound` and a concrete bind suggestion/remediation such as `bind_hint` or `gitcode-mcp repo add`.
- **Evidence Type**: CLI command execution against a locally built binary with temporary config/cache.

### 004-cmd-gitcode-mcp-task-4-add-doctor-command-scenario-3
- **Description**: With no token → 'no token configured' + available sources.
- **Actor**: Operator running `gitcode-mcp doctor` with token-related environment variables unset.
- **Expected Outcome**: The report includes `no_token_configured` or `no token configured` and lists available credential sources.
- **Evidence Type**: CLI command execution against a locally built binary with temporary config/cache.

## Decommission Verification

### DECOMM-004-001: decommission-11
- **Target**: Runtime audit reporting only config and credential status without repo/cache/index/MCP readiness detail.
- **Verification**: `gitcode-mcp doctor --format json` must include full readiness dimensions beyond runtime audit: config, cache, repo, credential/token, live provider, auth probe, sync/last-sync, index, and MCP transport.
