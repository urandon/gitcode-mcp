# Design Package Component: operations-security

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Operations And Security

## Summary
Operations and security owns secure process startup for `gitcode-mcp`: configuration loading, credential sourcing, global mode selection, startup dependency handoff, and diagnostic routing. The component work is limited to the two approved deltas: adding startup configuration support and changing `cmd/gitcode-mcp` to dispatch CLI or MCP stdio mode with resolved dependencies.

## Top-Level Alignment
This component implements the approved `cmd/gitcode-mcp` responsibility: default CLI mode, `--mcp` stdio mode, startup configuration, dependency handoff to CLI/MCP routes, and stderr-only diagnostics for MCP startup. It does not own cache schema, GitCode adapter behavior, index builds, service business logic, CLI commands, or MCP tool definitions.

## Tasks

### Task 1: Add config startup API
Outcome IDs: outcome-1
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: add
Description: Add a project-owned `internal/config` startup support package for non-secret runtime settings used by `cmd/gitcode-mcp` and later adapter/cache initialization. The package resolves cache paths, lock paths, GitCode base URL, timeout, max response size, retry count, and output format while keeping the GitCode token environment-only. It provides operations-security startup support without taking ownership of cache, service, adapter, CLI command behavior, or MCP tool behavior.
Existing Behavior / Reuse: The current scaffold delegates directly from `cmd/gitcode-mcp` to `internal/cli.Execute` and has no config loader, config structs, override merge, token accessor, or diagnostic redaction API. Reuse standard-library JSON, path, duration, and environment handling; leave existing CLI behavior untouched.
Detailed Design: Add `internal/config` as an operations-security support package with public types `Config`, `Overrides`, and `Source`, plus functions `Default() Config`, `Load(src Source, overrides Overrides) (Config, error)`, `Token(src Source) string`, and `RedactDiagnostic(message string, src Source) string`. `Config` contains only safe-to-serialize fields: `CachePath`, `LockPath`, `GitCodeBaseURL`, `DefaultTimeout`, `MaxResponseSize`, `MaxRetries`, and `Format`; it must not contain any token or secret-bearing field. `Overrides` carries CLI-provided non-secret values such as cache path, timeout, max size, and format, with zero values meaning “not set”; merge order is defaults, optional config file, then CLI overrides. `Source` abstracts environment lookup, home/config paths, and file reads so tests can cover default config path, `$GITCODE_CONFIG`, malformed JSON, unreadable files, and token redaction without touching real user state. Config-file semantics are fixed: absent default config path means use defaults; explicit `$GITCODE_CONFIG` path missing, unreadable, or malformed returns a redacted error; default config path present but malformed returns a redacted error. `Token` reads only `GITCODE_TOKEN` from `Source`; `RedactDiagnostic` removes the token value and config-derived sensitive context before any startup error is written to stderr. `internal/config` may be imported by `cmd/gitcode-mcp` and test packages, but it must not import `internal/cache`, `internal/gitcode`, `internal/service`, `internal/cli`, or `internal/mcp`.
Acceptance Criteria: Actor trigger: a developer runs startup config tests. Target product surface/API: `internal/config.Load`, `internal/config.Token`, `internal/config.RedactDiagnostic`, and CLI override inputs consumed by `cmd/gitcode-mcp`. Expected outcome: a temporary config selected by `GITCODE_CONFIG` overrides `cache_path`; absent default config uses defaults; explicit missing/malformed `$GITCODE_CONFIG` returns a redacted error; malformed present default config returns a redacted error; `GITCODE_TOKEN` is available through `Token` but absent from serialized/logged `Config`; a `--timeout 10s` override wins over configured `30s`. Executable evidence: `go test ./internal/... -run TestConfigLoading` and `go test ./internal/... -run TestCLIFlagOverride` pass.
Workload: 0.8 MM

### Task 2: Change entrypoint dependency handoff
Outcome IDs: outcome-1
Outcome Role: primary_product
Decommission IDs: none
Change Type: change
Description: Change `cmd/gitcode-mcp` from unconditional CLI delegation to a startup orchestrator that parses global flags, builds a named startup dependency bundle, and selects exactly one runtime route. The default route hands effective config and service dependencies to the CLI route; the MCP route starts the project-owned stdio MCP startup seam and reserves stdout for JSON-RPC frames. This task owns process startup and dependency handoff only, not MCP tool definitions or service business logic.
Existing Behavior / Reuse: Current `cmd/gitcode-mcp` calls `cli.Execute(os.Args[1:], os.Stdout, os.Stderr)` for every invocation. Reuse `internal/cli.Execute` as a compatibility fallback only until the CLI package exposes a dependency-aware runner seam; reuse the project-owned `internal/mcp` startup seam for MCP stdio mode. The current source has no `internal/config` package, no `internal/mcp` package, and no startup dependency bundle, so those seams are confirmed absent.
Detailed Design: Add a startup parser in `cmd/gitcode-mcp` that recognizes global `--mcp`, `-h/--help`, `--version`, `--cache-path`, `--timeout`, `--max-size`, and `--format` before transport dispatch. After `config.Load`, construct a named `StartupDeps` bundle in the entrypoint startup layer. `StartupDeps` contains the safe `config.Config`, the env-only token from `config.Token`, cache startup settings (`CachePath`, `LockPath`), GitCode adapter startup settings (`GitCodeBaseURL`, `DefaultTimeout`, `MaxResponseSize`, `MaxRetries`, token), index/service handles or constructor seams, and route dependencies for CLI and MCP. The bundle does not implement cache, GitCode, index, or service internals; it only carries resolved startup inputs and typed placeholders/factories until those owning packages provide concrete constructors. Map `Config.CachePath` and `Config.LockPath` to the cache constructor seam, `Config.GitCodeBaseURL`, `Config.DefaultTimeout`, `Config.MaxResponseSize`, `Config.MaxRetries`, and `Token` to the GitCode adapter constructor seam, cache/gitcode/index handles to the service constructor seam, and the resulting service handle plus config to the CLI and MCP route seams.

For CLI mode, define a target dependency-aware route such as `cli.Run(ctx, args, stdout, stderr, cli.RuntimeDeps{Config, Service})` or an equivalent runner interface accepted by the entrypoint. Until that seam exists, provide a compatibility adapter in the startup orchestration that receives `StartupDeps`, exposes the effective cache path/timeout to route tests, and delegates remaining args to `internal/cli.Execute` so existing commands such as `search` still follow the current CLI path. For MCP mode, call a concrete `internal/mcp` stdio startup seam such as `mcp.RunStdio(ctx, stdin, stdout, stderr, mcp.RuntimeDeps{Config, Service})` or equivalent constructor; the seam must accept an MCP initialize JSON-RPC request on stdin and return a JSON-RPC response on stdout containing server info. If full service/cache/gitcode/index constructors are not yet available, `StartupDeps` carries a minimal initialize-only service placeholder accepted by `internal/mcp`; later constructors replace the placeholder without changing `cmd` routing or the dependency handoff shape. Startup errors, config errors, malformed global flags, and dependency initialization failures in MCP mode write redacted diagnostics only to stderr and exit nonzero with no non-JSON stdout. Help ordering is explicit: `gitcode-mcp --help` writes normal startup/CLI help to stdout and documents `--mcp`; `gitcode-mcp --mcp --help` is handled before the MCP server starts, writes MCP startup help to stderr, leaves stdout empty, and exits zero so MCP JSON-RPC stdout is never contaminated.
Acceptance Criteria: Actor trigger: a developer builds and invokes the binary in default and MCP startup paths. Target product surface/runtime route: `cmd/gitcode-mcp` process entrypoint and its `StartupDeps` handoff to selected route. Expected outcome: `go build ./...` compiles without circular imports; `gitcode-mcp --help` writes help to stdout and includes `--mcp`; `gitcode-mcp --mcp --help` writes startup help to stderr and leaves stdout empty; `gitcode-mcp search "test"` reaches the default CLI route through the dependency-aware runner or compatibility adapter; a default-mode startup test uses a temporary config or `--cache-path`/`--timeout` override and asserts the selected CLI route observes the effective cache path and timeout from `StartupDeps`; `gitcode-mcp --mcp` starts stdio MCP mode, accepts a JSON-RPC initialize request on stdin, writes a JSON-RPC response with server info on stdout, and an MCP-mode startup test asserts the MCP route observes the same effective cache path/timeout without emitting the token; MCP startup/config-error tests verify stderr receives redacted diagnostics and stdout contains no non-JSON diagnostic text. Executable evidence: `go build ./...`, an entrypoint default-mode dependency-handoff test, a help-routing test, an MCP stdio initialize/server-info test, and an MCP dependency-handoff/redaction test pass.
Workload: 1.1 MM

## Cross-Cutting Constraints
- Credentials are environment-only — `GITCODE_TOKEN` is read through `internal/config.Token`, never persisted in config files, never stored in serializable `Config`, never stored in the visible dependency bundle representation, and never emitted in diagnostics or tests because the repository must remain public-safe.
- `internal/config` is startup support only — it does not become a new architecture peer and must not import cache, gitcode, service, cli, or mcp packages, preserving the approved dependency model.
- MCP stdout is JSON-RPC-only after MCP mode selection — startup diagnostics, redacted config errors, malformed flags, and dependency failures go to stderr to preserve stdio transport correctness.
- Startup dependency handoff is structural, not ownership transfer — operations-security maps config/token to cache, GitCode, index, service, CLI, and MCP constructor seams without implementing those packages’ internals.
- Audit persistence is handed off — durable audit entries remain owned by the approved `sync_events`/service/cache design; startup only routes diagnostics and does not create audit records.
- Telemetry is not introduced — startup/config loading adds no network emission and routine reads remain offline unless a later explicit telemetry feature is approved.

## Data And Control Flow
- Process starts — `cmd/gitcode-mcp` reads raw args and detects global help/version before runtime dispatch; `--mcp --help` exits before server startup with help on stderr and empty stdout.
- Configuration resolves — `cmd/gitcode-mcp` calls `internal/config.Load`; defaults, optional config file, and CLI overrides are merged deterministically, while token access remains separate through `internal/config.Token`.
- Startup dependencies are built — `cmd/gitcode-mcp` constructs `StartupDeps` from safe config plus env-only token and maps cache path/lock path, GitCode settings, index/service constructor seams, and route dependencies before choosing CLI or MCP.
- CLI mode dispatches — `cmd/gitcode-mcp` passes `StartupDeps` to the dependency-aware CLI route or compatibility adapter, which then invokes existing `internal/cli.Execute` until the CLI-owned runner seam is available.
- MCP mode dispatches — `cmd/gitcode-mcp` passes `StartupDeps` to the `internal/mcp` stdio startup seam, reserves stdout for JSON-RPC, and routes all diagnostics to stderr.

## Component Interactions
- `cmd/gitcode-mcp` -> `internal/config` — calls `Load` for non-secret startup settings and `Token` for env-only GitCode token handoff.
- `cmd/gitcode-mcp` -> `StartupDeps` — constructs the named dependency bundle carrying effective config, cache path/lock path, GitCode settings/token, index/service constructor seams, and CLI/MCP route dependencies.
- `cmd/gitcode-mcp` -> `internal/cli` — selects default CLI mode and hands effective config/service dependencies to a dependency-aware CLI route or compatibility adapter that preserves `cli.Execute` behavior.
- `cmd/gitcode-mcp` -> `internal/mcp` — starts the stdio MCP startup seam in `--mcp` mode with effective startup dependencies; stdout is reserved for JSON-RPC frames and diagnostics go to stderr.
- `StartupDeps` -> `internal/gitcode` — later adapter initialization receives base URL, timeout, retries, max response size, and env-only token without persisting credentials.
- `StartupDeps` -> `internal/cache` — later cache initialization receives cache path and lock path only, with no credential-bearing state.
- `StartupDeps` -> `internal/service` and `internal/index` — later service construction receives cache, GitCode, and index handles through constructor seams; startup does not implement service or index behavior.

## Rationale
The approved architecture assigns process entry and mode selection to `cmd/gitcode-mcp`, and the component impact assigns secure config, credential handling, and startup dependency injection to operations-security. Existing source lacks config loading, MCP dispatch, and dependency handoff, so this component needs one bounded config API and one entrypoint startup change while leaving cache, service, adapter, CLI command internals, and MCP tool semantics to their owning components.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0438-run_attempt-1/final_message.txt`
