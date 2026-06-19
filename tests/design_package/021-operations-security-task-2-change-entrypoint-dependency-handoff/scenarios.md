# Validation Scenarios: 021 operations-security task 2 change entrypoint dependency handoff

## 021-operations-security-task-2-change-entrypoint-dependency-handoff-scenario-1

Actor trigger: a developer builds and invokes the binary in default and MCP startup paths.

Concrete executable checks:
- `go build ./...` must compile the repository without circular imports.
- A locally built `gitcode-mcp` binary must support default help, MCP help, default CLI routing, and MCP stdio startup with no network access.

## 021-operations-security-task-2-change-entrypoint-dependency-handoff-scenario-2

Target product surface/runtime route: `cmd/gitcode-mcp` process entrypoint and its `StartupDeps` handoff to selected route.

Concrete executable checks:
- Entrypoint tests must assert the default route receives effective cache path and timeout through `StartupDeps`.
- Entrypoint tests must assert the MCP route receives effective cache path and timeout through `StartupDeps` without emitting the token.

## 021-operations-security-task-2-change-entrypoint-dependency-handoff-scenario-3

Expected outcome: `go build ./...` compiles without circular imports; `gitcode-mcp --help` writes help to stdout and includes `--mcp`; `gitcode-mcp --mcp --help` writes startup help to stderr and leaves stdout empty; `gitcode-mcp search "test"` reaches the default CLI route through the dependency-aware runner or compatibility adapter; a default-mode startup test uses a temporary config or `--cache-path`/`--timeout` override and asserts the selected CLI route observes the effective cache path and timeout from `StartupDeps`; `gitcode-mcp --mcp` starts stdio MCP mode, accepts a JSON-RPC initialize request on stdin, writes a JSON-RPC response with server info on stdout, and an MCP-mode startup test asserts the MCP route observes the same effective cache path/timeout without emitting the token; MCP startup/config-error tests verify stderr receives redacted diagnostics and stdout contains no non-JSON diagnostic text.

Concrete executable checks:
- `gitcode-mcp --help` exits zero, writes `--mcp` to stdout, and writes no stderr.
- `gitcode-mcp --mcp --help` exits zero, writes MCP startup help to stderr, and writes empty stdout.
- `gitcode-mcp --cache-path <tmp>/cache.db search test` reaches CLI command handling and does not fail as an unknown startup command.
- `gitcode-mcp --mcp --cache-path <tmp>/mcp.db --timeout 10s` accepts a JSON-RPC `initialize` request and returns JSON-RPC server info with `serverInfo.name == "gitcode-mcp"` on stdout.
- `GITCODE_CONFIG=<missing>` and `GITCODE_TOKEN=<sentinel>` in MCP mode fail with stderr diagnostics that do not contain the token and stdout that is empty.

## 021-operations-security-task-2-change-entrypoint-dependency-handoff-scenario-4

Executable evidence: `go build ./...`, an entrypoint default-mode dependency-handoff test, a help-routing test, an MCP stdio initialize/server-info test, and an MCP dependency-handoff/redaction test pass.

Concrete executable checks:
- `go test ./cmd/gitcode-mcp ./internal/... -run 'TestEntrypoint|TestConfigLoading|TestCLIFlagOverride|TestIntegration' -count=1` passes.
- `go test ./... -count=1` passes.
- `git diff --check` passes.
