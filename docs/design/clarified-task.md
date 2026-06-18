# Request Formalization Revision 1

## Main user request information
Design the complete architecture for `gitcode-mcp`, a standalone public Go CLI and MCP tool that provides cache-first, offline-capable read access to GitCode tracker/wiki data and replaces the current ad-hoc plaintext-repo agent knowledge layer. The first product slice is read-first: source ingest, local cache, search/get/backlinks/resolve/sync-status, deterministic export/diff, then MCP read tools. Writes to GitCode are explicit, idempotent, logged, and optional. The architecture must define the Go package layout, cache model, GitCode adapter boundary, MCP tool surface, CLI command surface, sync semantics, fixture strategy, testing strategy, RAG readiness boundary, and a one-week implementation plan. The design must model the current agent plaintext read path as a behavioral exemplar and define the minimum replacement bar: an agent can do coordinator intake, task lookup, handoff review, stale pointer search, and source citation using only `gitcode-mcp` cache/MCP reads when the network is offline.

## Decomposed tasks

### Task 1: Define Go package/module layout
Description: Propose a Go module layout (`internal/` packages) that grows from the current scaffold (`cmd/gitcode-mcp`, `internal/cli`) to support cache, adapter, MCP server, search, export, and indexing without overengineering. Define package boundaries, interfaces, and dependency direction.
Acceptance Criteria: A developer runs `go build ./...` from the repo root and all packages compile cleanly without circular imports. The package layout document lists every package, its single responsibility, and its public interface surface.

### Task 2: Define the local cache data model
Description: Design the SQLite schema and in-memory record model covering: records (sources, tasks, pages, decisions, handoffs), full-text index, identity map (stable id, local path, remote aliases), links/backlinks, remote revisions, sync events, conflicts, chunks, and provenance from derived projections back to raw documents.
Acceptance Criteria: A developer runs a Go test that opens an in-memory SQLite database, inserts a source record, inserts a task record, inserts a link between them, queries backlinks by target id, and receives the correct source record. The test also inserts a chunk with byte offsets and heading path and verifies that `source_id` plus `content_hash` uniquely identifies the chunk's parent revision.

### Task 3: Define the GitCode adapter boundary
Description: Define the Go interface(s) for GitCode tracker/wiki API calls: auth, pagination, rate limits, issue CRUD, wiki page CRUD, comments, attachments, search, and failure modes under bad network conditions. Reference `https://docs.gitcode.com/docs/apis/` for `/api/v5` behavior. Call out API-discovery gaps where public docs are insufficient and propose fixture/reverse-engineering steps.
Acceptance Criteria: A developer runs a contract test against sanitized HTTP fixtures (no credentials, no internal URLs) that exercises the adapter's issue-fetch method: the test serves fixture JSON over a local HTTP server, calls the adapter, and asserts structured issue records match expected fields (id, title, body, status, labels, created_at, updated_at). A separate timeout test verifies that the adapter returns a typed `ErrNetworkUnavailable` error when the upstream does not respond within a configurable deadline.

### Task 4: Define the MCP server boundary and read-first tool surface
Description: Define the MCP server lifecycle, transport, and at least these read-first tools: `search_tasks`, `get_task`, `get_page`, `backlinks`, `resolve_id`, `sync_status`, `export_snapshot`, `diff_snapshot`. Define the JSON-RPC request/response shapes, error codes, and how each tool maps to internal cache/adapter services. The server reads from the local cache for routine calls; live network is optional.
Acceptance Criteria: A developer runs an MCP server integration test that starts the server on a local stdio or HTTP transport, sends a `tools/list` request, and receives a JSON-RPC response containing tool definitions for all eight tools. A second test sends a `tools/call` for `resolve_id` with a known stable id and receives the correct local record with id, path, and remote alias fields populated.

### Task 5: Define CLI commands and service mapping
Description: Define every CLI command (`ingest`, `index`, `search`, `get`, `snippet`, `backlinks`, `tasks`, `tracks`, `link-check`, `stale-index`, `recent`, `sync-status`) with flags, args, output modes (compact path:line:snippet for humans, JSON for machines), and how each maps to the same internal services as MCP without requiring an MCP server process.
Acceptance Criteria: A developer runs `gitcode-mcp search "backlog" --format json` and receives valid JSON output with at least one result record containing `id`, `path`, `title`, and `snippet` fields when cache data exists. Running `gitcode-mcp get DOC-123` outputs the record with id, path, title, body, and status to stdout.

### Task 6: Define cache freshness and sync semantics
Description: Define the state machine for cache records: stale reads, explicit refresh, conflict records, idempotency keys, retry policy with backoff, local lock files for concurrent access, and audit logs. Define how `sync_status` computes freshness per record and how `sync` ingests/updates without losing local-only data.
Acceptance Criteria: A developer runs a test that inserts a stale cache record, calls `sync_status` to confirm staleness, then runs `sync` with valid remote data (from fixtures) and observes that the record is updated, the sync event is logged with an idempotency key, and a subsequent `sync_status` call reports the record as fresh. A concurrent-access test acquires a local lock file, attempts a second `sync` from another process, and verifies the second call exits with a lock-contention error without corrupting the cache.

### Task 7: Define public-safe fixture strategy
Description: Define how sanitized GitCode API response fixtures are captured, stored, and used in tests. Rules: no credentials, no internal URLs, no non-public source names. Define the fixture directory layout (e.g., `fixtures/api/v5/issues/`) and a capture/sanitize script that redacts `Authorization` headers, hostnames, and private project identifiers.
Acceptance Criteria: A developer runs `go test ./...` and every adapter contract test passes using only fixture files under `fixtures/`. The `scripts/sanitize-fixtures.sh` script runs against a raw captured response directory and produces redacted copies; a test verifies that no sanitized fixture contains the string `Authorization`, an internal hostname pattern, or a non-public project name.

### Task 8: Define testing strategy
Description: Define the test pyramid: unit tests for cache/index/link resolution (fast, no network), golden export tests (deterministic byte output), adapter contract tests over sanitized fixtures, MCP tool integration tests (local transport), and integration tests gated behind explicit credentials (opt-in via env var or flag). Define test fixture and golden-file conventions.
Acceptance Criteria: A developer runs `go test ./... -short` and all unit, contract, and golden tests pass in under 10 seconds with no network access. Running `go test ./... -run Integration` with `GITCODE_TEST_TOKEN` set exercises live API calls and skips cleanly (no panic, no fake pass) when the env var is unset.

### Task 9: Define the RAG readiness boundary
Description: Define the exact chunk model (chunk id, source id, source revision/content hash, byte offsets, line start/end, heading path, text, normalized text, inherited metadata, outbound links, resolved aliases) and provenance model that must be stored now so semantic retrieval can be added later without cache migration. Define what is deferred: embedding provider integration, vector database selection, reranking, semantic answer generation, chat memory, model-specific tuning. Define how a later RAG layer composes with existing tools: semantic search proposes candidate chunks, but `resolve_id`, `get_source`, `get_snippet`, backlinks, and sync status remain authoritative for citation.
Acceptance Criteria: A developer runs a chunking test that ingests a markdown source record, produces deterministic chunks with correct byte offsets, line numbers, heading paths, and inherited metadata. The test verifies that re-running chunking on the same source produces identical chunk ids. The chunk table schema supports a future `embedding` column without requiring a migration of existing chunk rows.

### Task 10: Define agent knowledge-layer replacement contract
Description: Model the current agent plaintext read path and define the exact MCP/CLI tool mapping that replaces each shell-equivalent workflow: `find` → `list_sources`, `rg -n` → `search_sources`, `rg --files` → list by kind/id, `sed -n` → `get_snippet`. Define the query patterns for coordinator intake, task lookup, handoff review, stale pointer search, and source citation. Produce a shell-equivalent query mapping table.
Acceptance Criteria: A developer follows a documented walkthrough: starting from a cold cache, they run `gitcode-mcp ingest` on test fixture sources, then execute `gitcode-mcp search_sources "backlog"`, `gitcode-mcp list_sources --kind task --status ready`, `gitcode-mcp get_source DOC-123`, and `gitcode-mcp source_backlinks DOC-123`. Each command produces output semantically equivalent to the shell workflow it replaces. An offline test confirms that all commands complete without network access.

### Task 11: Define derived index/build pipeline
Description: Define the incremental indexing pipeline: content hash, frontmatter parse, heading parse, outbound links extraction, backlink computation, id alias resolution, status extraction, changed-file routing, and stale-index detection. Define the derived indexes (source ledger, track index, task backlog, acceptance ledger, open questions, backlink graph, broken-link report) and how they are generated from raw cache records.
Acceptance Criteria: A developer runs `gitcode-mcp index --full` on a cache populated with test fixture sources and receives exit code 0. The developer then runs `gitcode-mcp stale-index` and receives a JSON report containing the count of stale backlinks and the list of affected source ids. A subsequent `gitcode-mcp index --incremental` on unchanged sources completes without rewriting unchanged derived records and reports zero new stale items.

### Task 12: Define GitCode write semantics
Description: Define how writes to GitCode tracker/wiki (create/update issue, create/update wiki page, comment, label) are exposed as explicit CLI commands and optional MCP write tools. Define idempotency key generation, write-ahead log, conflict detection, and how failed writes are retried or dismissed. Gate all writes behind explicit commands; never trigger them during routine reads.
Acceptance Criteria: A developer runs a test that calls the GitCode adapter write method with a mock HTTP server. The test sends a create-issue request, verifies the HTTP request includes an `Idempotency-Key` header, and on a 409 Conflict response from the server, the adapter returns a typed `ErrConflict` containing both the local and remote payloads without automatically overwriting either side.

### Task 13: Define cache/sync failure-mode behavior
Description: Produce a failure-mode table covering: network timeout during sync, partial response, rate-limit hit, auth expiry, remote id collision, local cache corruption, concurrent write conflict, missing remote record, and oversized attachment. For each mode, define the error type, user-visible message, cache state after failure, and recovery action.
Acceptance Criteria: A developer runs a test suite that exercises each failure mode against the adapter and cache layers. For network timeout, the test verifies the cache remains unchanged and the error message includes the record id and retry suggestion. For rate-limit hit, the test verifies the adapter returns the `Retry-After` value in the error type and no partial data is written to cache.

### Task 14: Define one-week implementation plan
Description: Produce a day-by-day implementation plan with milestones that delivers a useful first version without broad migration work. Each day has a concrete deliverable and verification command. The plan covers: Day 1 — cache schema + ingest; Day 2 — CLI search/get/backlinks; Day 3 — indexing + derived projections; Day 4 — MCP server + read tools; Day 5 — export/diff + diagnostics; Day 6 — integration hardening + fixture cleanup; Day 7 — documentation + minimum-replacement-bar walkthrough.
Acceptance Criteria: At the end of each simulated day, the documented verification command passes. The Day 7 walkthrough exercises the minimum replacement bar: `ingest` → `search_sources` → `get_source` → `source_backlinks` → `sync_status` all complete offline and produce correct output for a coordinator intake, task lookup, and handoff review scenario described in the plan.

## Assumptions
- The current Go scaffold (Go 1.21+, single `internal/cli` package) is a suitable starting point; no migration from another language is needed.
- SQLite via `modernc.org/sqlite` (pure Go, no CGo) is acceptable as the primary cache store; the user has not specified alternative database requirements.
- The MCP server will use stdio transport initially; HTTP/SSE transport is deferred.
- GitCode API follows REST patterns with token-based auth and standard pagination (page/per_page); exact pagination envelope will be discovered during API research.
- "Source" records can originate from local markdown files (ingested via filesystem walk), GitCode tracker exports, and GitCode wiki exports; all share a common normalized record shape.
- The "plaintext knowledge layer" being replaced is a git repository of markdown files with frontmatter, hand-maintained indexes, and stable ids like `TASK-0202`; the cache must accommodate this shape.
- Local lock files use OS-level file locks (`flock` on Linux/macOS); cross-platform lock behavior is acceptable for a developer tool.
- The first version targets a single GitCode instance; multi-instance federation is a non-goal.
- Competitor repositories (MCP protocol spec, gh CLI, GitLab CLI) will be cloned into `ai/artifacts/` automatically by the run plan; we assume network access for that clone step but not for routine tool operation.

## Runner Evidence
- Final message: `runa/calls/call-0006-run_request_formalization/attempt-1/final_message.txt`
