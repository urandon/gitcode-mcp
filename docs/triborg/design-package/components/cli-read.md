# Design Package Component: cli-read

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: CLI Read Surface

## Summary
The CLI Read Surface is detailed because Component Impact marks `cli-read` as `detailed` and assigns it two owned deltas: completing cache-first CLI read commands and exposing shared canonical read payloads for MCP parity. The component owns CLI read command handlers, read request/result structs, deterministic JSON/text renderers, and repo-scoped service interfaces used by CLI reads.

## Top-Level Alignment
This component implements Request Task 5’s offline CLI read-path contract and supplies Request Task 6’s CLI-side canonical payloads for MCP parity. It does not own MCP JSON-RPC handlers, transports, live sync, cache schema migrations, chunk generation algorithms, or snapshot storage semantics; it calls those component services through repo-scoped read interfaces.

## Tasks

### Task 1: Complete CLI read handlers
Outcome IDs: outcome-5
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: The CLI read command surface must expose the complete offline product contract after fixture sync/index. This task adds or completes component-local command handlers, read request/result structs, service calls, and renderers for deterministic cache-first reads. Export and diff remain CLI read commands in this task, with snapshot resolution delegated to `snapshot_diff`.
Existing Behavior / Reuse: Reuse the existing `cmd/gitcode-mcp` entrypoint and `internal/cli` command dispatch concept as the production CLI path. Reuse any existing read-oriented command concepts already present for `list`, `search`, `get`, snippets, backlinks, recent changes, link checks, stale-index reporting, cache status, chunk listing, export, and diff, but confirm missing or scaffold-only behavior before adding handlers. New component-local additions are typed read request/result structs, repo-scoped read service interfaces, deterministic JSON/text renderers, and CLI integration coverage; cache/index/snapshot implementations remain owned by their components. This task does not add `sync-status`; the CLI JSON counterpart for MCP `sync_status` belongs to Task 2 because it is justified by `cli-read-delta-2` / Request Task 6.
Detailed Design: Add or extend component-local CLI read registrations under the production command dispatch for canonical command names exactly `list`, `search`, `get`, `get-snippet`, `backlinks`, `recent`, `link-check`, `stale-index`, `cache-status`, `list-chunks`, `export`, and `diff`. Accept only these Task 5 compatibility aliases: `snippet` and `snippets` for `get-snippet`, because Request Task 5 names snippets as a product capability while the architecture names `get-snippet`; no source-style or underscore aliases are added in this task. Each handler parses `--repo <repo_id>` before constructing a typed request; missing repo returns typed `validation-failed`, unknown configured repo returns typed `not-found`, and cross-repo alias ambiguity is delegated to repo/cache read services and surfaced as typed `conflict` or `not-found`.

Add or complete typed request/result structs at the CLI read service boundary: `ListRequest/ListResult`, `SearchRequest/SearchResult`, `GetRequest/GetResult`, `SnippetRequest/SnippetResult`, `BacklinksRequest/BacklinksResult`, `RecentRequest/RecentResult`, `LinkCheckRequest/LinkCheckResult`, `StaleIndexRequest/StaleIndexResult`, `CacheStatusRequest/CacheStatusResult`, `ListChunksRequest/ListChunksResult`, `ExportRequest/ExportResult`, and `DiffRequest/DiffResult`. Every request carries `RepoID string`, stable pagination where applicable, source filters where applicable, and explicit addressing fields; every result carries `repo_id`, deterministic item ordering, warning metadata, and typed error class metadata when rendered. Add `ChunkSummary` fields for `repo_id`, chunk id, source type, source id, record id, content hash, line range, byte range, heading path, snapshot id when present, and warnings.

Handlers call exactly one repo-scoped read service method and never call the GitCode adapter, live HTTP client, sync code, or raw storage queries directly. `get-snippet` supports line-range snippets through `--line-start/--line-end` and chunk snippets through `--chunk-id`; if both addressing modes are supplied, validation returns `validation-failed`. `list-chunks` orders chunks by repo, source type, record id, range, then chunk id. `cache-status` reports row counts, last sync timestamp, index freshness, stale-index state, and warnings. `export` and `diff` handlers delegate stored snapshot lookup, citation assembly, missing-index warnings, and unknown snapshot errors to the snapshot service while preserving deterministic CLI rendering.
Acceptance Criteria: After a developer runs fixture sync/index and disables network access, invoking the production CLI entrypoint with `--repo <repo_id>` for canonical `list`, `search`, `get`, `get-snippet`, `backlinks`, `recent`, `link-check`, `stale-index`, `cache-status`, `list-chunks`, `export`, and `diff` returns deterministic repo-scoped text and JSON output. The same integration suite invokes exactly the supported Task 5 aliases `snippet` and `snippets` and verifies they resolve to the same behavior as `get-snippet`. Expected visible/state outcome is issue and wiki records in list/search/get flows, chunk citations and source ranges for snippet/list-chunks/export flows, row counts and index freshness in cache-status, stored snapshot behavior in export/diff, and explicit stale or missing-index warnings instead of silent omissions. Executable evidence is a CLI integration test over sanitized fixtures through the production CLI entrypoint that verifies missing `--repo` returns `validation-failed`, unknown repo returns `not-found`, cross-repo alias collision is rejected or disambiguated by repo/cache services, text/JSON ordering and fields are deterministic, and no read command performs live network access.
Workload: 1.2 MM

### Task 2: Share read parity payloads
Outcome IDs: outcome-6
Outcome Role: supporting_evidence
Decommission IDs: decommission-1
Change Type: change
Description: MCP parity depends on CLI reads being produced from shared canonical read payloads rather than CLI-only formatting or shell-only coordinator workflows. This task changes the read service boundary so CLI JSON output is the deterministic reference payload MCP handlers can wrap for Request Task 6 read tools. `mcp_server` remains responsible for JSON-RPC tool schemas, handler wiring, protocol errors, and transport envelopes.
Existing Behavior / Reuse: Reuse the production `cmd/gitcode-mcp` entrypoint and `internal/cli` dispatch from Task 1, plus any existing service concepts for search, list, get, backlinks, and sync-status if present. New component-local additions are canonical result structs and JSON renderer semantics for snippet, recent changes, link check, stale-index report, cache status, search/list chunks, backlinks, list/search/get, and repo-aware `sync-status`. This task replaces any shell-only coordinator read construction path for the covered Task 6 queries with shared service payload construction, while keeping human CLI commands as adapters over the same service. MCP `content`, `structuredContent`, JSON-RPC envelopes, tool schemas, and transport metadata remain owned by `mcp_server`.
Detailed Design: Introduce a canonical read payload construction rule: each covered CLI command handler builds a typed repo-scoped request, calls exactly one read service method for product state, receives a canonical result struct, and passes that result to either the CLI JSON renderer or CLI text renderer. Add or complete service methods `GetSnippet`, `RecentChanges`, `LinkCheck`, `StaleIndex`, `CacheStatus`, `SearchChunks`, `ListChunks`, `SyncStatus`, plus shared methods for existing list/search/get/backlinks payloads. Add the CLI command `sync-status` and compatibility alias `sync_status` only in this task as the CLI JSON counterpart for MCP `sync_status`; this command returns repo-scoped sync metadata and does not trigger sync or live network activity.

Shared payload fields include `repo_id`, source type, source id, record id, chunk id, pagination, counts, UTC timestamps, warnings, stale-index flags, and typed error classes. Stable JSON semantics are defined on canonical result structs through field names/tags and deterministic service ordering. MCP may wrap the canonical payload in protocol-specific `structuredContent` and response envelopes, but must not rename, omit, or reorder shared payload fields. CLI text formatting and stderr/stdout behavior are presentation adapters and are verified separately from canonical JSON payload equivalence.

For `decommission-1`, replace the coordinator shell-only read workflow for covered Request Task 6 queries by making the shared read service the only product-state construction path for snippets, chunk search/listing, backlinks, recent changes, link checks, stale-index reports, cache status, and sync status. Existing shell commands remain as human-facing adapters over the same service and are not removed from the product. The negative invariant is enforced by paired parity tests proving no covered read query has only a shell-only coordinator path. `export` and `diff` are intentionally not MCP parity requirements in this component task; they remain CLI-only read payloads from Task 1 and are handed off to `snapshot_diff`.
Acceptance Criteria: An MCP JSON-RPC/server test invokes Request Task 6 read tools over the same fixture cache used by CLI tests: `get_snippet`, `recent_changes`, `link_check`, `stale_index_report`, `cache_status`, `search_chunks` or `list_chunks`, repo-aware `sync_status`, and existing read tools for list/search/get/backlinks where present. A paired CLI test invokes the equivalent production CLI command with `--format json` and the same `--repo <repo_id>`, including `sync-status` and `sync_status` as the CLI counterpart for MCP `sync_status`. Expected response outcome is byte-equivalent canonical JSON for shared read payload fields, identical repo scoping, stale/missing-index warnings, UTC timestamp normalization, pagination, and typed error classes, excluding MCP JSON-RPC envelope fields and CLI text presentation. Executable evidence is the paired MCP server/API parity test plus CLI JSON integration test, and the test suite verifies that export/diff are not required MCP parity tools for this component while remaining valid CLI-only read payloads through Task 1.
Workload: 1.0 MM

## Cross-Cutting Constraints
- Cache-first reads only — CLI read handlers must never call the GitCode adapter or trigger live network activity; all reads use cache/index/snapshot services after explicit sync.
- `repo_id` on every read — each request/result and error path is scoped to the configured repository so aliases remain isolated across repositories.
- Deterministic payloads — CLI JSON is the reference payload for MCP read parity, with stable ordering, UTC timestamp normalization, warning fields, and typed error classes.
- Missing index is explicit — absent or stale chunk/index state is represented as warning metadata in cache-status, stale-index, snippet, list-chunks, export, and diff results.
- CLI/MCP boundary stays separate — `cli-read` owns canonical read payload construction and CLI rendering, while `mcp_server` owns JSON-RPC envelopes, tool schemas, and transports.

## Data And Control Flow
- User invokes a CLI read command — `cmd/gitcode-mcp` enters `internal/cli` dispatch — the command handler normalizes only the exact supported canonical command or compatibility alias before parsing flags.
- CLI handler resolves repo scope — handler requires `--repo <repo_id>`, asks repo/cache services to validate configured repo scope, and maps missing/unknown/colliding identity cases to typed errors before rendering.
- Read service handles product state — service method queries cache/index/snapshot through repo-scoped interfaces — result structs are ordered deterministically and include warning/error metadata before rendering.
- CLI output is rendered — JSON renderer encodes the canonical result struct, while text renderer formats the same result for humans without changing product semantics.
- MCP parity consumes the handoff — `mcp_server` calls the same service methods and wraps the returned canonical payload in JSON-RPC `structuredContent`, preserving shared fields while owning protocol-specific envelopes.
- Chunk/snippet query arrives — read service resolves source id or chunk id inside the repo scope — stale-index, missing-index, not-found, conflict, and validation-failed states are returned as typed warnings/errors.
- Snapshot read command arrives — CLI read surface delegates stored snapshot resolution and diff semantics to snapshot services — export/diff results include chunks, citations, warnings, or not-found errors without adding MCP parity scope.

## Component Interactions
- `cli-read` -> `cache_sync` — read service methods consume repo-scoped records, links, sync metadata, row counts, and cache-empty/not-found/conflict errors without performing network calls.
- `cli-read` -> `repo_binding` — CLI read handlers and services rely on repo resolution, configured repo validation, and alias collision behavior rather than duplicating identity rules in command handlers.
- `cli-read` -> `index_chunking` — snippet, stale-index, cache-status, search-chunks, and list-chunks service methods consume deterministic chunks, source ranges, hashes, and index freshness flags.
- `cli-read` -> `snapshot_diff` — export and diff commands delegate stored snapshot resolution, citation assembly, warnings, and unknown-id errors to snapshot services while preserving CLI render determinism.
- `cli-read` -> `mcp_server` — `cli-read` provides canonical read request/result structs, stable JSON field semantics, service methods, and parity fixtures; `mcp_server` consumes those payloads and owns JSON-RPC handler wiring, tool schemas, `content`/`structuredContent` envelopes, and transports.

## Rationale
The component is affected because Request Task 5 makes offline CLI reads the primary user-facing product surface and Component Impact assigns `cli-read` ownership of the cache-first command surface. Request Task 6 also depends on `cli-read` to provide canonical read payloads for MCP parity, while keeping MCP transport and handler implementation outside this component.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0404-run_attempt-1/final_message.txt`
