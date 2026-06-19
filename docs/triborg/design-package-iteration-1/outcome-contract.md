# Outcome Contract

Schema Version: `triborg.outcome-contract.v1`

## outcome-1
- Request Task: 1
- Role: `primary_product`
- Request Item: Propose a Go package/module layout that can grow from the current scaffold without overengineering
- Target Surface: Go module packages under internal/
- Actor/Trigger: Developer runs go build ./... from repo root
- Expected Outcome: All packages compile cleanly without circular imports
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: no_mocks


## outcome-2
- Request Task: 2
- Role: `supporting_evidence`
- Request Item: Define the cache model: records, identity map, source aliases, local paths, remote ids, backlinks, full-text/search index, sync status, conflicts, and deterministic export snapshots
- Target Surface: SQLite schema and in-memory record model in internal/cache/
- Actor/Trigger: Developer runs Go test that opens in-memory SQLite, inserts source/task/link records, queries backlinks
- Expected Outcome: Backlink query returns correct source record; chunk insert verifies source_id + content_hash uniqueness
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: no_mocks


## outcome-3
- Request Task: 3
- Role: `supporting_evidence`
- Request Item: Define the GitCode adapter boundary for tracker/wiki API discovery, auth, pagination, comments, attachments, wiki pages, issue CRUD, rate limits, and failure modes under bad network conditions
- Target Surface: internal/gitcode/ adapter interface
- Actor/Trigger: Developer runs contract test against sanitized HTTP fixtures
- Expected Outcome: Adapter returns structured issue records matching fixture fields; timeout test returns typed ErrNetworkUnavailable
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: external_dependencies_only


## outcome-4
- Request Task: 4
- Role: `supporting_evidence`
- Request Item: Define the MCP server boundary and read-first tool surface
- Target Surface: MCP server JSON-RPC transport (stdio/HTTP)
- Actor/Trigger: Developer runs MCP server integration test sending tools/list and tools/call for resolve_id
- Expected Outcome: tools/list returns all eight tool definitions; resolve_id returns correct local record with id, path, remote alias
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: no_mocks


## outcome-5
- Request Task: 5
- Role: `supporting_evidence`
- Request Item: Define CLI commands and how they map to the same internal services as MCP
- Target Surface: CLI surface: gitcode-mcp search, get, snippet, backlinks, tasks, tracks, link-check, stale-index, recent, sync-status
- Actor/Trigger: Developer runs gitcode-mcp search "backlog" --format json with cache data present
- Expected Outcome: Valid JSON output with result records containing id, path, title, snippet
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: no_mocks


## outcome-6
- Request Task: 6
- Role: `supporting_evidence`
- Request Item: Define cache freshness and sync semantics for offline-first operation
- Target Surface: sync_status and sync commands, lock file mechanism, sync events table
- Actor/Trigger: Developer runs sync test: insert stale record, call sync_status, run sync with fixture data, call sync_status again
- Expected Outcome: sync_status reports stale then fresh; sync event logged with idempotency key; concurrent sync exits with lock-contention error
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: external_dependencies_only


## outcome-7
- Request Task: 7
- Role: `supporting_evidence`
- Request Item: Define public-safe fixture strategy: sanitized GitCode responses, no credentials, no internal URLs, no non-public source names
- Target Surface: fixtures/ directory, scripts/sanitize-fixtures.sh, adapter contract tests
- Actor/Trigger: Developer runs go test ./... and sanitize-fixtures.sh
- Expected Outcome: All adapter contract tests pass using only fixture files; no sanitized fixture contains Authorization, internal hostname, or non-public project name
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: no_mocks


## outcome-8
- Request Task: 8
- Role: `supporting_evidence`
- Request Item: Define testing strategy: unit tests for cache/index/link resolution, golden exports, adapter contract tests over fixtures, MCP tool tests, integration tests gated behind explicit credentials
- Target Surface: go test ./... -short and go test ./... -run Integration
- Actor/Trigger: Developer runs go test ./... -short and go test ./... -run Integration with and without GITCODE_TEST_TOKEN
- Expected Outcome: Short tests pass in under 10s with no network; Integration tests skip cleanly when token unset, run live when set
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: external_dependencies_only


## outcome-9
- Request Task: 9
- Role: `supporting_evidence`
- Request Item: Define the RAG readiness boundary: what must be stored and exposed now for future semantic retrieval
- Target Surface: Chunk model in internal/cache/, corpus export boundary
- Actor/Trigger: Developer runs chunking test: ingest markdown source, produce deterministic chunks, re-run chunking on same source
- Expected Outcome: Identical chunk ids on re-run; chunk table schema supports future embedding column without migration of existing rows
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: no_mocks


## outcome-10
- Request Task: 10
- Role: `supporting_evidence`
- Request Item: Model the current agent read path and define shell-equivalent query mapping
- Target Surface: gitcode-mcp ingest, search_sources, list_sources, get_source, source_backlinks commands
- Actor/Trigger: Developer runs documented walkthrough: ingest fixtures, then run search_sources, list_sources, get_source, source_backlinks offline
- Expected Outcome: Each command produces output semantically equivalent to the shell workflow it replaces; all complete without network
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: no_mocks


## outcome-11
- Request Task: 11
- Role: `supporting_evidence`
- Request Item: Define derived index/build pipeline: incremental indexing, content hash, frontmatter parse, heading parse, backlinks, stale detection
- Target Surface: gitcode-mcp index --full, gitcode-mcp index --incremental, gitcode-mcp stale-index commands
- Actor/Trigger: Developer runs index --full on populated cache, then stale-index, then index --incremental on unchanged sources
- Expected Outcome: index --full exits 0; stale-index reports stale backlink count and affected ids; index --incremental completes without rewriting unchanged records and reports zero new stale items
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: no_mocks


## outcome-12
- Request Task: 12
- Role: `supporting_evidence`
- Request Item: Define GitCode write semantics: explicit, idempotent, logged, optional; define idempotency key generation, conflict detection, retry
- Target Surface: GitCode adapter write methods, write-ahead log/sync_events table
- Actor/Trigger: Developer runs test calling adapter write method with mock HTTP server; sends create-issue, server returns 409 Conflict
- Expected Outcome: HTTP request includes Idempotency-Key header; adapter returns typed ErrConflict with local and remote payloads, no automatic overwrite
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: external_dependencies_only


## outcome-13
- Request Task: 13
- Role: `supporting_evidence`
- Request Item: Define failure-mode table for poor network availability and define error types, user-visible messages, cache state, recovery actions
- Target Surface: Adapter error types (ErrNetworkUnavailable, ErrRateLimited, ErrConflict, etc.), cache integrity after each failure mode
- Actor/Trigger: Developer runs test suite exercising each failure mode against adapter and cache layers
- Expected Outcome: Network timeout: cache unchanged, error includes record id and retry suggestion. Rate-limit: error includes Retry-After, no partial data written to cache
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: external_dependencies_only


## outcome-14
- Request Task: 14
- Role: `primary_product`
- Request Item: Produce a one-week implementation plan with milestones that gives a useful first version
- Target Surface: Day-by-day verification commands: go test, go run, gitcode-mcp CLI commands
- Actor/Trigger: Developer executes each day's verification commands sequentially
- Expected Outcome: Each day's verification command passes; Day 7 walkthrough exercises ingest -> search_sources -> get_source -> source_backlinks -> sync_status offline for coordinator intake, task lookup, handoff review
- Evidence Type: CLI command
- Freshness: current source build
- Mock Policy: external_dependencies_only
