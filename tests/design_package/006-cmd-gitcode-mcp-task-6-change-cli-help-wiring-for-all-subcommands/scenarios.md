# Validation Scenarios: Change CLI help wiring for all subcommands

## Scenario Inventory

### SCN-001: 006-cmd-gitcode-mcp-task-6-change-cli-help-wiring-for-all-subcommands-scenario-1
- **Description**: gitcode-mcp --help → lists all subcommands, exits 0. gitcode-mcp sync --help → valid help text, exits 0. gitcode-mcp bind --help, index --help, auth --help → all return valid help text, exit 0.
- **Actor**: Operator running `--help` on root and registered subcommands.
- **Expected Outcome**: 
  - `gitcode-mcp --help` prints startup-level usage with global flags and exits 0.
  - `gitcode-mcp sync --help` prints sync-specific help text containing "sync" and exits 0.
  - `gitcode-mcp index --help` prints index-specific help text containing "index" and exits 0.
  - `gitcode-mcp auth --help` prints auth-specific help text containing "auth" and exits 0.
  - Note: `bind` is not a registered command in this codebase; `repo add` serves the binding function. `gitcode-mcp repo --help` prints repo-specific help text and exits 0.
  - All `--help` paths produce no `invalid_query` diagnostic on stderr.
- **Evidence Type**: CLI command execution.

### SCN-002: 006-cmd-gitcode-mcp-task-6-change-cli-help-wiring-for-all-subcommands-scenario-2
- **Description**: No invalid-query diagnostic on any --help path.
- **Actor**: Operator running `--help` on every registered subcommand.
- **Expected Outcome**: None of the following produce `invalid_query` in stdout or stderr: `gitcode-mcp <subcommand> --help` for all subcommands in the registered commands list. All exit 0. All produce non-empty help text on stdout and empty stderr.
- **Evidence Type**: CLI command execution; Go test assertions.

## Decommission Verification

### DECOMM-009: decommission-9
- **Target**: CLI subcommand --help paths returning invalid-query diagnostics.
- **Verification**: Every registered subcommand `--help` path returns valid help text and exits 0. The `helpRequested` boolean in `options` struct is set when `-h`/`--help` flags are parsed. Before service dispatch, `helpRequested` is checked and `printCommandHelp`/`printLocalSubcommandHelp` is called, bypassing service creation. No `invalid_query` error class appears on any `--help` path. Root `--help` lists all subcommands in the `commands` slice. Unknown commands still produce `unknown command` diagnostic on stderr (not `invalid_query`), preserving error behavior.
