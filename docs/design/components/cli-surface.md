# Component Design: CLI Surface

## Summary
The CLI surface is affected because the existing CLI is only a stub command dispatcher, while the approved architecture requires cache-first product commands that call the shared service layer directly. This design changes `internal/cli` into the human and script-facing command tree for read workflows, explicit sync, diagnostics, export/diff, and gated write commands.

## Top-Level Alignment
`internal/cli` remains a transport adapter sibling to `internal/mcp` and imports the shared service layer rather than implementing business logic. It owns argument parsing, help text, output formatting, exit-code mapping, and dispatch to service methods defined by the approved architecture.

## Tasks

### Task 1: Command tree implements service dispatch
Outcome IDs: outcome-5, outcome-10
Outcome Role: primary_product
Decommission IDs: decommission-1, decommission-3, decommission-4
Change Type: change
Description: Replace the current stub-only CLI dispatcher with a command registry that executes cache-first read commands, explicit sync/index/export diagnostics, and gated GitCode write commands. The local CLI role is to parse command arguments, call the shared service methods, format output as human text or JSON, and convert typed service errors into stable user-visible exit behavior. This task keeps all domain logic in the service layer and makes the CLI a thin product surface.
Existing Behavior / Reuse: Existing `Execute(args, stdout, stderr)` help/version behavior and command-name scaffold are reused as the entrypoint shape. Existing stub behavior that prints “not implemented yet” for known commands is replaced after confirming the current component has no implemented service dispatch, no command-specific flag parsing, no JSON formatter, and no write gate. Existing `cmd/gitcode-mcp` invocation shape remains compatible because the CLI still exposes one top-level execution function.
Detailed Design: Add a component-local command registry keyed by command name, where each entry owns usage text, flag parsing, argument validation, service method selection, output formatter selection, and success/error exit code mapping. Add a CLI runtime object that carries `stdout`, `stderr`, parsed global options such as `--format`, cache/config flags passed through to service construction, and a service dependency that exposes the command-facing methods: `Ingest`, `Index`, `SearchSources`, `ListSources`, `GetSource`, `GetSnippet`, `GetBacklinks`, `Tasks`, `Tracks`, `LinkCheck`, `StaleIndex`, `Recent`, `GetSyncStatus`, `SyncToCache`, `ExportSnapshot`, `DiffSnapshot`, `CreateIssue`, `UpdateIssue`, `CreatePage`, `UpdatePage`, `AddComment`, and `AddLabel`. Implement read command handlers for `ingest`, `index --full|--incremental`, `search`, `search_sources`, `list`, `list_sources`, `get`, `get_source`, `snippet`, `get_snippet`, `backlinks`, `source_backlinks`, `tasks`, `tracks`, `link-check`, `stale-index`, `recent`, and `sync-status`; shell-replacement aliases call the same handlers as their short CLI equivalents so `search_sources` and `search` cannot diverge. Implement sync/export/write handlers for `sync`, `export`, `diff`, `create-issue`, `update-issue`, `create-page`, `update-page`, `add-comment`, and `add-label`; write handlers require explicit command invocation, validate required payload fields before calling service, and never run from read command code paths. Add formatter functions for default human output and `--format json`: search emits compact `path:line:snippet` rows by default and JSON records with `id`, `path`, `title`, and `snippet`; get emits full record fields including `id`, `path`, `title`, `body`, and `status`; backlink and link-check outputs include resolved ids and aliases; write outputs include idempotency key/evidence returned by service without exposing credentials. Enforce the decommission negative invariant by making shell-equivalent product paths (`search_sources`, `list_sources`, `get_source`, `source_backlinks`, `get_snippet`) resolve through service-backed cache reads only, not by spawning `find`, `rg`, or `sed`; local markdown links are not treated as the primary resolution result when service returns identity-map aliases.
Acceptance Criteria: A developer trigger runs `gitcode-mcp search "backlog" --format json` against a populated cache through the CLI product surface and receives valid JSON containing at least one result with `id`, `path`, `title`, and `snippet`; executable evidence is `go test ./internal/cli/... -run TestSearchJSON`. A developer trigger runs `gitcode-mcp get DOC-123` through the CLI product surface and stdout contains the record `id`, `path`, `title`, `body`, and `status`; executable evidence is `go test ./internal/cli/... -run TestGetSource`. A developer trigger runs `gitcode-mcp --help` and sees all required command names and aliases registered, including read, sync/index/export, and explicit write commands; executable evidence is `go test ./internal/cli/... -run TestAllCommandsRegistered`. A developer trigger runs the offline shell-replacement walkthrough through CLI routes `ingest`, `search_sources`, `list_sources`, `get_source`, and `source_backlinks`; expected outcome is semantically equivalent cache output without network or shell subprocess use, with executable evidence `go test ./internal/cli/... -run TestMinimumReplacementBar`.
Workload: 1.5 MM

## Cross-Cutting Constraints
- CLI commands must call the shared service layer and not duplicate cache, GitCode adapter, or index business logic — this preserves CLI/MCP behavioral equivalence.
- Routine read commands must be cache-first and must not touch the network — this enforces the offline read invariant and shell-workflow decommission.
- GitCode writes must be exposed only as explicit CLI commands — this preserves the architecture rule that reads never trigger writes.
- Human and JSON output formats must remain stable enough for agents and tests to consume — this is the CLI compatibility surface.

## Data And Control Flow
- User invokes `gitcode-mcp <command>` — `internal/cli` parses global options and command flags — command handler validates args before service dispatch.
- Read command handler calls service read method — service reads cache — CLI formatter writes human text or JSON to stdout.
- Shell-replacement alias command calls the same handler as the canonical command — CLI preserves one implementation path for equivalent workflows.
- Explicit write command validates payload and calls service write method — service performs adapter/cache work — CLI prints returned evidence or conflict error.

## Component Interactions
- `internal/cli` -> `internal/service` — CLI imports and dispatches to service methods for all product behavior; service owns cache, adapter, sync, export, and index logic.
- `cmd/gitcode-mcp` -> `internal/cli` — entrypoint passes process args and stdio to CLI execution while preserving the binary surface.
- `internal/cli` -> `internal/mcp` — no direct runtime dependency; both surfaces must map equivalent read operations to the same service methods.
- `internal/cli` -> `internal/gitcode` — no direct adapter import for domain behavior; GitCode writes and sync flow through service so CLI stays a transport adapter.

## Rationale
The component impact marks `cli-surface` as detailed and identifies one concrete local delta: replacing the existing stub scaffold with a full command tree, output formatting, and service dispatch. The current source confirms that required functionality is absent, so a detailed product task is needed for `internal/cli`.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0120-run_attempt-1/final_message.txt`
