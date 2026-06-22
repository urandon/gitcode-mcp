# Design Package Component: docs

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Documentation

## Summary
Four documentation deltas: create `docs/live-readiness.md` operator guide and `docs/sanitization.md` public-safety rules; update `docs/architecture.md` with provider-selection and credential flow; update `docs/cache-and-sync-model.md` with live sync semantics and cache migration policy. Every task verifies documentation correctness against the actual `--help` output, CLI behavior, and source anchors from the approved architecture.

## Top-Level Alignment
This component delivers task-13's documentation delta. Each documented flow anchors to the approved architecture: provider dispatch via `--live` flag (a1), credential pipeline via env→keychain→none fallback (a5), live sync semantics via cache sync events (a7), cache schema versioning via `PRAGMA user_version` at version 3 (a6), and sanitization rules via the redaction filter contract (a11). Acceptance criteria exercise documentation correctness by comparing documented commands, flags, schemas, and redaction patterns against the actual binary and cache behavior.

## Tasks

### Task 1: Create live-readiness operator guide
Outcome IDs: outcome-13
Outcome Role: supporting_evidence
Decommission IDs: decommission-13
Change Type: add
Description: Create `docs/live-readiness.md` as a step-by-step operator guide referencing concrete CLI subcommands and their documented flags. Every documented command must be verifiable against the current binary's `--help` text and fixture-mode behavior.
Existing Behavior / Reuse: No `docs/live-readiness.md` exists. `docs/secrets.md` covers token storage; `docs/repo-binding.md` covers repo management. This guide ties them into a single workflow using cross-references rather than duplicating content. All commands reference `cmd/gitcode-mcp/` subcommands already registered in the CLI.
Detailed Design: Create `docs/live-readiness.md` with 9 sections:

**1. Prerequisites.** Lists env vars (`GITCODE_TOKEN`, `GITCODE_MCP_CACHE_DIR`, `GITCODE_MCP_CONFIG_DIR`) and cross-references `docs/secrets.md` for token setup, `docs/repo-binding.md` for repo management.

**2. Bind a repo.** Documents `gitcode-mcp bind --repo-owner "YOUR_OWNER" --repo "YOUR_REPO"`. Uses sanitized placeholders only.

**3. Verify credentials.** Documents `gitcode-mcp auth status`. Pipeline: env → keychain → none. Verifiable via `auth status --help`.

**4. Live sync.** Documents `gitcode-mcp sync --live`. Paginated fetch of issues, comments, wiki pages; rate-limit handling; re-sync delta. Verifiable via `sync --help`.

**5. Live write.** Documents `gitcode-mcp create-issue --live --idempotency-key "ik-001" --title "Test" [--body "Body"] [--dry-run]`. Verifiable via `create-issue --help`.

**6. Search.** Documents `gitcode-mcp search_sources "query"`. Empty set on no-match. Verifiable via `search_sources --help`.

**7. MCP server.** Documents `gitcode-mcp --mcp` start and all 7 MCP read tools. Kind enums include `issue` and `wiki`. Verifiable via `--help`.

**8. Doctor diagnostics.** Documents `gitcode-mcp doctor`. Output includes version, config, cache schema version, repo binding, token source (redacted), last sync, index freshness, MCP transport.

**9. Troubleshooting.** Cross-references `docs/troubleshooting.md`.

Acceptance Criteria:
- **AC-1:** An operator follows `docs/live-readiness.md` steps 2 and 3 in sequence: runs `gitcode-mcp bind --help` and `gitcode-mcp auth status --help`. Each `--help` output matches the documented flags and descriptions. Evidence: `local command` — trigger is running `--help` for each documented subcommand; target surface is stdout help text; expected outcome is flag/description match between doc and `--help` output.
- **AC-2:** An operator runs `gitcode-mcp auth status` with no `GITCODE_TOKEN` set. The output lists available token sources (env, keychain, none) without revealing secrets, matching the documented credential pipeline order in section 3. Evidence: `local command` — trigger is `auth status` without token; target surface is stdout; expected outcome is available-sources list matching doc.
- **AC-3:** An operator runs `gitcode-mcp sync --help`. The `--help` output shows `--live` flag with description matching the documented behavior in section 4. Evidence: `local command` — trigger is `sync --help`; target surface is stdout; expected outcome is `--live` flag present with matching description.
- **AC-4:** An operator runs `gitcode-mcp create-issue --help`. The `--help` output shows `--live`, `--dry-run`, `--idempotency-key`, `--title`, and `--body` flags with descriptions matching the documented behavior in section 5. Evidence: `local command` — trigger is `create-issue --help`; target surface is stdout; expected outcome is all documented flags present with matching descriptions.
- **AC-5:** An operator runs `gitcode-mcp doctor` against a fixture cache with no repo binding. The output reports "no repo bound" and suggests the `bind` command, matching the documented behavior in section 8. Evidence: `local command` — trigger is `doctor` with no binding; target surface is stdout; expected outcome is "no repo bound" diagnostic with `bind` suggestion matching doc.

Workload: 0.25 MM

### Task 2: Document provider and credential flow
Outcome IDs: outcome-13
Outcome Role: supporting_evidence
Decommission IDs: decommission-13
Change Type: change
Description: Update `docs/architecture.md` to document the provider-selection decision tree and the credential fallback pipeline. Every documented mode, flag, and source priority must be traceable to the approved architecture's provider-selection predicate and fallback order contract.
Existing Behavior / Reuse: `docs/architecture.md` lists components with text data-flow. The "GitCode adapter" row exists. This task adds new sections for provider selection and credential flow without rewriting existing component descriptions. The provider-selection predicate and credential fallback order are defined in the approved architecture's Admission And Degradation Rules.
Detailed Design: Modify `docs/architecture.md` with three additions:

**(a) Provider Selection section.** Add `## Provider Selection` documenting: `fixture` mode (default when no `--live` flag, used by `go test ./...`), `live` mode (activated by `--live` with valid token via provider a4), and `unavailable` mode (`--live` set but no credentials → `ProviderUnavailableError`). The selection predicate is: `--live` flag + credentials → live; `--live` + no credentials → unavailable; no `--live` → fixture. Mode resolution happens once at command start; no mid-process switching.

**(b) Credential Flow section.** Add `## Credential Pipeline` documenting: priority 1 = `GITCODE_TOKEN` env var, priority 2 = macOS Keychain (darwin-gated, build-tag-gated, no-op elsewhere), priority 3 = none (yields `AuthError`). The `auth status` command reports which source produced the token and a redacted preview. The redaction contract ensures tokens, Authorization headers, and private repo coordinates never appear in output.

**(c) Components table update.** Rename the existing "GitCode adapter" row to "GitCode adapter (fixture + live providers)" or add a note about provider modes.

Acceptance Criteria:
- **AC-1:** A reviewer reads `docs/architecture.md` Provider Selection section and traces the three modes to the `--help` output of `gitcode-mcp sync`. The `sync --help` text references the `--live` flag matching the documented predicate. Evidence: `local command` — trigger is `sync --help` compared to architecture doc; target surface is doc text and help output; expected outcome is `--live` flag described consistently in both.
- **AC-2:** A reviewer reads the Credential Pipeline section's priority chain (env → keychain → none) and compares it to `gitcode-mcp auth status --help`. The help text lists env and keychain as available sources and omits secrets. Evidence: `local command` — trigger is `auth status --help`; target surface is stdout; expected outcome is source list matching doc order.
- **AC-3:** An operator runs `go test ./...` with no `GITCODE_TOKEN` set. All tests pass, confirming the documented statement that fixture mode is the default and `go test ./...` stays offline. Evidence: `local command` — trigger is `go test ./...` without live env vars; target surface is test output; expected outcome is PASS with no live API calls.

Workload: 0.10 MM

### Task 3: Document sync and migration policy
Outcome IDs: outcome-13
Outcome Role: supporting_evidence
Decommission IDs: decommission-13
Change Type: change
Description: Update `docs/cache-and-sync-model.md` to document live sync event semantics, partial failure behavior, and the cache migration policy derived from `PRAGMA user_version` at schema version 3. Every documented version number and compatibility rule must match the approved architecture's Cache Schema Versioning contract.
Existing Behavior / Reuse: `docs/cache-and-sync-model.md` defines candidate tables and sync principles. The approved architecture specifies `PRAGMA user_version` as the versioning mechanism, current schema version 3, migration from version 2 to 3 (in-place, data preserved), and version 1 as blocked (requires re-initialization). This task documents those existing architectural contracts.
Detailed Design: Modify `docs/cache-and-sync-model.md` with three sections:

**(a) Live Sync Semantics.** Add `## Live Sync Semantics`: `sync --live` routes through the live GitCode provider (a4). Paginated GET to `/api/v4/projects/:id/issues`, `/api/v4/projects/:id/wikis`, `/api/v4/projects/:id/issues/:iid/notes`. Completion produces a `SyncEvent` with start/end timestamps and remote version tracking. Re-sync: unchanged records produce zero delta; no duplicates. Each page/resource fetched independently.

**(b) Partial Failure Handling.** Add `## Partial Failure Handling`: each page/resource fetched independently; successes committed to cache; failures collected and reported as `PartialSyncError` with success/failure counts. Rate-limit (429) and auth-failure (401/403) caught with clear diagnostics.

**(c) Cache Migration Policy.** Add `## Cache Migration`: schema version stored via `PRAGMA user_version` on SQLite cache. Current version: 3. Compatibility matrix: version 3 → normal open; version 2 → `migrate-cache` upgrades in place (data preserved); version 1 → blocked, reports incompatibility, recommends `reinit-cache`; version 0/unknown → blocked; future version → blocked, recommends binary upgrade. Migration is transaction-wrapped. The `migrate-cache` command performs version-2-to-3 upgrade.

Acceptance Criteria:
- **AC-1:** A reviewer reads the documented compatibility matrix (version 3 → open, version 2 → migrate, version 1 → blocked) and verifies it matches the behavior of opening a version-1 cache file with the current binary — the binary exits non-zero with an incompatibility message referencing detected version 1 and expected version 3. Evidence: `local command` — trigger is any command against a version-1 cache file; target surface is stderr + exit code; expected outcome is version mismatch diagnostic matching the documented matrix.
- **AC-2:** An operator runs `gitcode-mcp migrate-cache --help`. Help text documents the command as upgrading a version-2 schema to version 3, matching the documented migration path. Evidence: `local command` — trigger is `migrate-cache --help`; target surface is stdout; expected outcome is help text describing version-2-to-3 upgrade matching docs.
- **AC-3:** An operator runs `gitcode-mcp sync_status --help`. Help text references sync event timestamps and delta reporting, matching the documented live sync semantics. Evidence: `local command` — trigger is `sync_status --help`; target surface is stdout; expected outcome is help text referencing timestamps and delta reporting matching doc.

Workload: 0.15 MM

### Task 4: Create sanitization rules document
Outcome IDs: outcome-13
Outcome Role: supporting_evidence
Decommission IDs: decommission-13
Change Type: add
Description: Create `docs/sanitization.md` documenting the public-safety rules enforced by the redaction filter contract defined in the approved architecture's component a11 (`internal/diagnostics/`). Every documented surface type and replacement pattern must be verifiable against actual CLI redaction behavior.
Existing Behavior / Reuse: No `docs/sanitization.md` exists. The approved architecture's Redaction contract defines the policy: never log or display raw tokens, Authorization headers, private repo coordinates, or raw API response bodies. `README.md` line 9 states "Keep examples sanitized." `AGENTS.md` states "Keep credentials out of repository files, logs, fixtures, and test snapshots." This task consolidates scattered rules into a single reference.
Detailed Design: Create `docs/sanitization.md` with 5 sections:

**(1) Purpose.** The public-safety contract: no raw tokens, private URLs, owner/repo names, Authorization headers, or raw API response bodies appear in any output surface (CLI stdout/stderr, MCP tool responses, test output, fixtures, logs).

**(2) Redacted Surface Types.** Table documenting what is redacted and how: tokens (from `GITCODE_TOKEN` env var, `[REDACTED]` or `ghp_****abcd` preview), Authorization headers (full header value replaced), private repo coordinates (owner/repo names replaced with `[REDACTED]` in any output), raw API response bodies (sanitized before display), internal URLs (replaced when not approved).

**(3) Safe Replacement Patterns.** Documented placeholders: `$GITCODE_TOKEN` (env var reference in docs), `YOUR_OWNER`/`YOUR_REPO` (documentation placeholders), `[REDACTED]` (full redaction in output), redacted token preview format.

**(4) Surface-Specific Rules.** CLI output: all stdout/stderr passes through redaction before display. MCP tool responses: JSON bodies are sanitized. E2e test output: assertions confirm no sensitive fields. Fixtures: contain no real tokens or private coordinates.

**(5) Verification.** `go test ./...` passes with fixture-only providers and no live env vars. Unit tests for redaction behavior live in `internal/diagnostics/`.

**README.md update:** On line 9, append a link to `docs/sanitization.md` for the full public-safety rules.

Acceptance Criteria:
- **AC-1:** A reviewer reads `docs/sanitization.md` section 2 (Redacted Surface Types) and checks `gitcode-mcp auth status` output with `GITCODE_TOKEN` set. The token value appears as `[REDACTED]` or a partial preview, never the full string, matching the documented redaction behavior for tokens. Evidence: `local command` — trigger is `auth status` with token set; target surface is stdout; expected outcome is redacted token matching doc.
- **AC-2:** A reviewer reads section 2's entry on private repo coordinates and verifies that `gitcode-mcp doctor` output with a bound repo shows `[REDACTED]` for owner/repo fields, never the real coordinates, matching the documented rule. Evidence: `local command` — trigger is `doctor` with bound repo; target surface is stdout; expected outcome is redacted owner/repo matching doc.
- **AC-3:** A reviewer reads section 4 (Surface-Specific Rules) and confirms that fixture files under the project contain no real tokens or private coordinates. Evidence: `local command` — trigger is grep for token patterns in fixture files; target surface is file contents; expected outcome is zero matches for real token patterns.
- **AC-4:** A reviewer reads section 3 (Safe Replacement Patterns) and verifies that all documented placeholder values (`YOUR_OWNER`, `YOUR_REPO`, `$GITCODE_TOKEN`, `[REDACTED]`) match the actual form used in `docs/live-readiness.md` and other doc files. Evidence: `local command` — trigger is checking doc files for consistent placeholder usage; target surface is doc file content; expected outcome is placeholders match the documented patterns.

Workload: 0.15 MM

## Cross-Cutting Constraints
- All documentation must use sanitized placeholders (`YOUR_OWNER`, `YOUR_REPO`, `$GITCODE_TOKEN`) and never real private coordinates — implements the public-safety contract per the approved architecture's Redaction contract
- Documentation must cross-reference existing docs (`docs/secrets.md`, `docs/repo-binding.md`, `docs/troubleshooting.md`) rather than duplicating their content — preserves single-source-of-truth
- Every documented schema version, migration path, and compatibility rule must match the approved architecture's Cache Schema Versioning contract: current version 3, `PRAGMA user_version`, version-2-to-3 migration

## Data And Control Flow
- Operator reads `docs/live-readiness.md` → follows step-by-step guide → each step references a `cmd/gitcode-mcp/<command>.go` subcommand — linear read-then-execute flow verified by `--help` comparison
- Reviewer reads `docs/architecture.md` provider-selection tree → traces `--live` flag dispatch to provider mode → verifies against `sync --help` output — documentation-vs-implementation check
- Reviewer reads `docs/cache-and-sync-model.md` migration policy → checks `PRAGMA user_version` version 3, version-2 migration, version-1 block → verifies against `migrate-cache --help` and version-1 cache open behavior — documentation-vs-implementation check
- Reviewer reads `docs/sanitization.md` redaction rules → verifies each surface type against actual CLI output (token redaction in `auth status`, coordinate redaction in `doctor`, fixture cleanliness) — documentation-vs-implementation check

## Component Interactions
- `docs/live-readiness.md` → `cmd/gitcode-mcp/` subcommands (`bind`, `auth`, `sync`, `create-issue`, `search_sources`, `doctor`) — each documented command's flags and behavior must match `--help` output
- `docs/sanitization.md` → `internal/diagnostics/` redaction filter contract — every redacted surface type must match the actual redaction behavior of CLI and MCP output paths
- `docs/cache-and-sync-model.md` → `internal/cache/` schema versioning via `PRAGMA user_version` at version 3 — documented compatibility matrix must match cache open behavior
- `docs/architecture.md` → provider dispatch flow from `--live` flag to provider mode resolution — documented modes must match the actual selection predicate
- `docs/architecture.md` → credential pipeline from `os.Getenv("GITCODE_TOKEN")` through keychain to none — documented fallback order must match actual acquisition chain

## Rationale
The docs component is affected because iteration 3 introduces two new documentation files and requires updates to two existing docs to reflect the provider-selection architecture, credential pipeline, live sync semantics, cache migration policy, and sanitization rules. All four deltas are traceable to component-impact entries under `docs-delta-1` through `docs-delta-4`, each owned by request task 13. Every acceptance criterion verifies documentation correctness by comparing documented behavior against the actual `--help` output, CLI behavior, or cache/filesystem state of the current binary.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-1536-run_attempt-1/final_message.txt`
