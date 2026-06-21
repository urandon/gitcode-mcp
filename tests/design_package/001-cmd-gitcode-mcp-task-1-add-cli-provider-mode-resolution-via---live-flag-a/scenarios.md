# Validation Scenarios: CLI Provider Mode Resolution via --live flag

## Scenario Inventory

### SCN-001: 001-cmd-gitcode-mcp-task-1-add-cli-provider-mode-resolution-via---live-flag-a-scenario-1
- **Description**: Operator runs gitcode-mcp sync --live after bind --live → reaches GitCode API.
- **Actor**: Operator with GITCODE_TOKEN set and --live flag.
- **Expected Outcome**: The `--live` flag propagates through `resolveLiveClient` → `NewLiveProvider` → `service.NewWithClient`. Missing GITCODE_TOKEN produces a clear diagnostic referencing GITCODE_TOKEN. Valid token routes to live provider path.
- **Evidence Type**: CLI command execution + source code inspection.

### SCN-002: 001-cmd-gitcode-mcp-task-1-add-cli-provider-mode-resolution-via---live-flag-a-scenario-2
- **Description**: Operator runs gitcode-mcp sync (no --live) → fixture provider. go test ./... with no live env vars → all tests pass offline.
- **Actor**: Operator without --live flag, CI/CD without GITCODE_TOKEN or any live env vars.
- **Expected Outcome**: Without `--live`, `service.New(store)` uses `sanitizedFixtureClient{}` as the default provider. `go test ./...` passes with zero network calls and no live env vars set.
- **Evidence Type**: CLI command execution + test suite execution.

## Decommission Verification

### DECOMM-001: decommission-1
- **Target**: sanitizedFixtureClient hard-wired in service.New bypassing any provider selection.
- **Verification**: `service.New` still defaults to `sanitizedFixtureClient{}` as fallback. `main.go` injects a live client via `service.NewWithClient` when `--live` and token are present. The fixture-only path is a default, not the sole runtime path.

### DECOMM-002: decommission-2
- **Target**: fixture-only client being the sole runtime provider (no live path exists).
- **Verification**: `--live` flag activates live provider path through `resolveLiveClient` → `NewLiveProvider` → `NewWithClient`. `go test ./...` with no live env vars passes using fixture-only. Live path exists and is gated behind `--live` flag and `GITCODE_TOKEN`.
