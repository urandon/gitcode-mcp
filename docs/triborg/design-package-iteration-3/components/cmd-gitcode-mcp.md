# Design Package Component: cmd-gitcode-mcp

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: CLI Entrypoint

## Summary
CLI Entrypoint owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add CLI provider mode resolution via --live flag a
Outcome IDs: outcome-1
Outcome Role: primary_product
Decommission IDs: decommission-1, decommission-2
Change Type: add
Description: Implement the `CLI provider mode resolution via --live flag and root command wiring` delta inside `CLI Entrypoint`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `CLI Entrypoint` boundaries where available and add only the missing `CLI provider mode resolution via --live flag and root command wiring` behavior.
Detailed Design: Add or change `CLI provider mode resolution via --live flag and root command wiring` so it satisfies `Wire --live persistent flag on root command; resolve provider mode in PreRun to live/fixture/unavailable; inject into service.New; ensure sync without --live uses fixture default.`. Keep ownership inside `CLI Entrypoint`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Operator runs gitcode-mcp sync --live after bind --live → reaches GitCode API. Operator runs gitcode-mcp sync (no --live) → fixture provider. go test ./... with no live env vars → all tests pass offline..
Workload: 1 MM

### Task 2: Add auth status CLI command
Outcome IDs: outcome-2
Outcome Role: primary_product
Decommission IDs: decommission-3
Change Type: add
Description: Implement the `auth status CLI command` delta inside `CLI Entrypoint`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `CLI Entrypoint` boundaries where available and add only the missing `auth status CLI command` behavior.
Detailed Design: Add or change `auth status CLI command` so it satisfies `Add gitcode-mcp auth status command that queries credential pipeline, reports token source (env/keychain/none) with redacted value, lists available sources when none found.`. Keep ownership inside `CLI Entrypoint`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: gitcode-mcp auth status with GITCODE_TOKEN set → reports source 'env' with redacted value. With no token → lists available sources. With keychain token → reports source 'keychain' with redacted value..
Workload: 1 MM

### Task 3: Add Live write commands (create-issue, create-comm
Outcome IDs: outcome-4
Outcome Role: primary_product
Decommission IDs: decommission-5
Change Type: add
Description: Implement the `Live write commands (create-issue, create-comment, create-wiki-page)` delta inside `CLI Entrypoint`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `CLI Entrypoint` boundaries where available and add only the missing `Live write commands (create-issue, create-comment, create-wiki-page)` behavior.
Detailed Design: Add or change `Live write commands (create-issue, create-comment, create-wiki-page)` so it satisfies `Add create-issue, create-comment, create-wiki-page CLI commands gated by --live flag with --dry-run, --idempotency-key, and conflict detection support.`. Keep ownership inside `CLI Entrypoint`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: gitcode-mcp create-issue --live --idempotency-key 'ik-001' --title 'Test' → issue created with cached record. Repeat same command → 'already applied'. --dry-run → validates without remote call. Conflict scenario → conflict detected and reported..
Workload: 1 MM

### Task 4: Add doctor command
Outcome IDs: outcome-11
Outcome Role: primary_product
Decommission IDs: decommission-11
Change Type: add
Description: Implement the `doctor command` delta inside `CLI Entrypoint`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `CLI Entrypoint` boundaries where available and add only the missing `doctor command` behavior.
Detailed Design: Add or change `doctor command` so it satisfies `Add gitcode-mcp doctor command that aggregates subsystem diagnostics (version, config, cache, repo binding, token source, live provider, auth probe, last sync, index freshness, MCP transport) with public-safe redacted output.`. Keep ownership inside `CLI Entrypoint`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: gitcode-mcp doctor → reports version, config, cache, repo binding, token source (redacted), live provider reachable, auth probe, last sync, index freshness, MCP transport — all public-safe. With no binding → 'no repo bound' + bind suggestion. With no token → 'no token configured' + available sources..
Workload: 1 MM

### Task 5: Add migrate-cache CLI command
Outcome IDs: outcome-10
Outcome Role: primary_product
Decommission IDs: decommission-10
Change Type: add
Description: Implement the `migrate-cache CLI command` delta inside `CLI Entrypoint`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `CLI Entrypoint` boundaries where available and add only the missing `migrate-cache CLI command` behavior.
Detailed Design: Add or change `migrate-cache CLI command` so it satisfies `Add gitcode-mcp migrate-cache command that runs version-2-to-3 cache schema migration in a transaction, with backup prompt before upgrade.`. Keep ownership inside `CLI Entrypoint`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: gitcode-mcp migrate-cache against iter-2 cache → schema upgraded in place, data preserved. Iter-1 cache → incompatibility reported, re-initialization recommended..
Workload: 1 MM

### Task 6: Change CLI help wiring for all subcommands
Outcome IDs: outcome-9
Outcome Role: primary_product
Decommission IDs: decommission-9
Change Type: change
Description: Implement the `CLI help wiring for all subcommands` delta inside `CLI Entrypoint`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `CLI Entrypoint` boundaries where available and add only the missing `CLI help wiring for all subcommands` behavior.
Detailed Design: Add or change `CLI help wiring for all subcommands` so it satisfies `Audit and fix all subcommand --help paths so every registered subcommand returns valid help text and exits 0; root --help lists all subcommands.`. Keep ownership inside `CLI Entrypoint`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: gitcode-mcp --help → lists all subcommands, exits 0. gitcode-mcp sync --help → valid help text, exits 0. gitcode-mcp bind --help, index --help, auth --help → all return valid help text, exit 0. No invalid-query diagnostic on any --help path..
Workload: 1 MM

### Task 7: Change search_sources CLI command handler
Outcome IDs: outcome-7
Outcome Role: supporting_evidence
Decommission IDs: decommission-7
Change Type: change
Description: Implement the `search_sources CLI command handler` delta inside `CLI Entrypoint`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `CLI Entrypoint` boundaries where available and add only the missing `search_sources CLI command handler` behavior.
Detailed Design: Add or change `search_sources CLI command handler` so it satisfies `Fix search_sources CLI command to query the same cache/FTS backend as search_chunks; return empty result set on no match instead of cache_empty error.`. Keep ownership inside `CLI Entrypoint`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: gitcode-mcp search_sources 'test' after fixture sync/index → non-empty results. gitcode-mcp search_sources 'notfound' → empty result set, no error..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `CLI Entrypoint` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `CLI Entrypoint` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
