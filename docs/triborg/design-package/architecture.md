# Design Package Architecture

This file is copied from the approved Triborg design package during implementator preflight.

# Architecture

## Need
This architecture closes the gap between the current scaffold and a minimum dogfood slice by establishing repository-scoped identity, an explicit sync/bootstrap-to-index pipeline, deterministic read contracts for CLI and MCP, shared SQLite cache concurrency, GitCode write-path safety, snapshot/chunk integrity, public-safe docs, and credential-gated live-validation seams. Every component owns a cache-first contract, and every live network surface is gated behind explicit sync or write commands.

- Provide a `repo_id`-first scoping model so all cache, read, write, and MCP operations are bounded to configured GitCode repositories.
- Define the sync→cache→index→export pipeline with deterministic chunking and citation tracking.
- Deliver archive-only parity between CLI reads and MCP tool reads for coordinator usage.
- Enable shared-cache multi-client HTTP/SSE MCP serving with correlation diagnostics.
- Guarantee idempotent, auditable write paths that never report fake success.
- Keep all config, credentials, docs, fixtures, and diagnostics public-safe.
- Tag live GitCode API validation as optional, explicit, and credential-gated.

- RAG embeddings, vector DB, reranking — deferred; architecture makes chunk/citation data RAG-ready but does not add retrieval pipelines.
- Replacing `modernc.org/sqlite` with a file-per-document store — SQLite remains the sole cache engine.
- Network-tolerant background daemon sync — sync remains explicit CLI invocation; HTTP/SSE serves reads only from the live process.
- Replicating full GitCode UI features (attachments, labels, custom fields) — initial scope is issue body/comments and wiki page body/sections.
- OAuth device flow or browser-based token acquisition — token is provided via env or credential store; browser flows are follow-up work.

## Approach
### Overview
The system is composed of eight subsystems and two externals:

1. **Config & Credential Subsystem** — sources config from disk (YAML), environment variables, and platform credential stores (macOS Keychain, Linux D-Bus Secret Service, Windows Credential Manager). Provides a `locate`, `show --redacted`, and `doctor` surface. Token values are never logged or cached in plaintext.

2. **Repository Binding Model** — `repo_id` is the master scope key. A `repos` table stores repository metadata (owner, name, API base URL, enabled issue/wiki scopes, display name, aliases). All cache records, sync events, snapshots, and API calls carry `repo_id`. Alias collisions across repos are rejected at bind/read time.

3. **GitCode Adapter Seam** — an interface/port behind which live API calls are isolated. Two implementations: a live HTTP adapter (gated by credentials and `--live` flag) and a fixture adapter (reads from sanitized JSON files). Mocks for write-path tests live behind the same seam.

4. **Cache & Sync Bootstrap Subsystem** — a pure-Go SQLite database with WAL mode and busy timeout. Tables: `records` (issue/wiki body + metadata), `record_comments`, `identity_map`, `links`, `remote_revisions`, `sync_events`, `snapshots`, `snapshot_chunks`, and `audit_trail`. The `sync` command populates cache from the adapter, records sync events with idempotency keys, and triggers index population. Imported markdown projections are stored with a `provenance=projection` tag distinct from `provenance=remote`.

5. **Index & Chunking Subsystem** — consumes cache records and produces deterministic chunk sets. Default heading-delimited markdown chunker runs first; sliding-window chunker is an optional policy for changelogs/non-heading documents. Chunks carry source range, record id, repo id, snapshot id, and content hash. Missing-index state is surfaced as explicit warnings, never silently omitted.

6. **CLI Read/Write Surface** — provides explicit commands for list, search, get, snippets, backlinks, recent changes, link-check, stale-index, cache-status, list-chunks, export-snapshot, diff-snapshot, sync, and write operations (issue create/update, wiki page create/update, comment add). All reads are cache-first; writes require explicit `--dry-run` vs `--live` choice.

7. **MCP Server & Transport Subsystem** — exposes MCP resources and tools mirroring CLI read semantics. Two transports: stdio (single-client, local) and HTTP/SSE (multi-client, shared cache). The HTTP/SSE server binds to `localhost` by default, serves `/health`, `/ready`, `/sse` (SSE endpoint), and `/message` (JSON-RPC). Request correlation IDs are assigned and logged. The server holds a single reader/writer lock on the cache.

8. **Snapshot & Diff Subsystem** — stores indexed snapshots keyed by snapshot id. `export-snapshot` emits chunks and citations or explicit missing-index warnings. `diff-snapshot` resolves stored IDs and rejects unknown IDs with not-found errors.

9. **Docs & Dogfood Feedback Subsystem** — public-safe Markdown docs under `docs/` covering install, config, repo binding, sync, CLI reads, MCP setup, troubleshooting. A dogfood checklist ties each day-slice to a product check. Feedback is captured in a sanitized artifact with lint rules preventing secret/private-path leakage.

### Overall Invariants

- Every cache row and read response carries `repo_id`.
- No remote API call occurs without explicit user command (`sync`, write with `--live`, `doctor --live`, live validation).
- CLI read commands do not require network.
- Any MCP tool response for a read query is byte-identical to the equivalent CLI read for the same snapshot.
- Write operations either produce a durable audit row on success or return a typed error; the system cannot report success for a write that did not reach the remote adapter.
- The SQLite cache uses WAL mode; concurrent readers are always safe; writers serialize on a per-process lock.

### Architecture Diagram
```mermaid
flowchart TD
    subgraph External["External Systems"]
        User[User / Agent / MCP Client]
        GitCodeAPI[GitCode API]
        CredStore[OS Credential Store<br/>Keychain / D-Bus / WinCred]
    end

    subgraph Runtime["GitCode MCP Runtime"]
        ConfigCred[Config & Credential<br/>a1: YAML + env + credential-store<br/>locate / show --redacted / doctor]
        RepoBind[Repository Binding<br/>a2: repo_id master scope<br/>alias collision rejection]

        subgraph CacheLayer["Cache & Storage Layer"]
            CacheSync[Cache & Sync Bootstrap<br/>a3: SQLite WAL<br/>provenance tags / sync events / audit]
            IndexChunk[Index & Chunking<br/>a5: heading-delimited + sliding-window<br/>deterministic chunks with source ranges]
            SnapshotDiff[Snapshot & Diff<br/>a9: export with citations<br/>diff by stored IDs]
        end

        Adapter[GitCode Adapter Seam<br/>a4: live HTTP & fixture adapters<br/>redaction pipeline]

        CLIRead[CLI Read Surface<br/>a6: get / search / snippets / backlinks<br/>cache-first, no network]
        CLIWrite[CLI Write Surface<br/>a7: --dry-run / --live gating<br/>audit-gated success]

        MCPServer[MCP Server & Transport<br/>a8: stdio + HTTP/SSE<br/>health / ready / correlation IDs]
    end

    DocsDogfood[Docs & Dogfood<br/>a10: public-safe docs, checklist<br/>feedback artifact with lint]

    ConfigCred -->|read scoped repos| RepoBind
    ConfigCred -->|token lookup| CredStore

    RepoBind -->|repo_id filter on all queries| CacheLayer

    Adapter -->|live API calls (credential-gated)| GitCodeAPI
    Adapter -->|fixture responses (no network)| CacheSync

    User -->|sync / write --live| CLIWrite
    User -->|sync --issues --wiki| CacheSync
    User -->|read commands| CLIRead
    User -->|MCP JSON-RPC (stdio or HTTP/SSE)| MCPServer

    CLIWrite -->|dry-run or live call| Adapter
    CLIWrite -->|audit row + cache refresh| CacheSync

    CacheSync -->|populate records + events| IndexChunk
    IndexChunk -->|chunks + citations| SnapshotDiff

    CLIRead -->|cache-first queries| CacheSync
    CLIRead -->|chunk/snippet lookups| IndexChunk
    CLIRead -->|export / diff| SnapshotDiff

    MCPServer -->|tool call → cache query| CacheSync
    MCPServer -->|tool call → chunk query| IndexChunk
    MCPServer -->|sync_status| CacheSync

    CacheSync -->|health / readiness| MCPServer
    ConfigCred -->|doctor --runtime-audit| CacheSync
    ConfigCred -->|doctor --runtime-audit| MCPServer
    ConfigCred -->|doctor --runtime-audit| IndexChunk

    User -->|doc smoke test / feedback| DocsDogfood
    MCPServer -->|tool surface documented in| DocsDogfood
    CLIRead -->|command tour documented in| DocsDogfood
```

### Components
- `config_credential` — Config discovery, YAML+env+credential-store precedence, redacted display, auth status, diagnostics
- `repo_binding` — `repo_id` master scope key, repos table, alias collision rejection, scope-gated filters
- `cache_sync` — SQLite WAL cache, sync bootstrap, provenance tagging, idempotency keys, sync events, audit trail, concurrency ownership
- `gitcode_adapter` — Adapter seam for live HTTP and fixture adapters; write-path safety, redaction, optional live validation
- `index_chunking` — Deterministic chunking with heading-delimited default and sliding-window optional; chunk schema with source ranges
- `cli_read` — Cache-first CLI read commands (list, search, get, snippets, backlinks, recent, link-check, stale-index, cache-status, list-chunks, export, diff)
- `cli_write` — Write commands with `--dry-run`/`--live` gating, audit persistence, idempotency
- `mcp_server` — MCP tool parity, stdio and HTTP/SSE transports, health/readiness, correlation IDs, multi-client reads
- `snapshot_diff` — Snapshot storage, export with citations/warnings, diff by stored IDs, not-found on unknown
- `docs_dogfood` — Public-safe docs, dogfood checklist, feedback artifact, lint/validation

### Requirement Coverage
| Request Task | Architecture Resolution | Components | Interfaces / Flow | Risk | Validation |
|---|---|---|---|---|---|
| Task 1 | `doctor --runtime-audit` surfaces version, config, repo binding, cache state, MCP surface, sync/index readiness, and failure classes | Config, Cache, MCP Server | CLI → config loader → cache inspect → MCP transport check | Config/cache mismatch in dev | CLI test: `doctor --runtime-audit --repo <repo_id>` against fixture cache; verify output includes version, config path, cache row counts, MCP tool list, sync/index status, and actionable failure classes |
| Task 2 | `repo_id` as master scope key in repos table, identity_map, records, sync_events, snapshots; alias collision rejection at bind+read time | Repository Binding, Cache | repo add/status CLI → repos table → all cache queries filter by repo_id | Multi-repo alias collision edge cases | CLI integration test: add two repos with overlapping aliases; verify get by alias is disambiguated or rejected |
| Task 3 | Config sourced from YAML + env + credential store; `config init`, `config locate`, `config show --redacted`, `auth status`, `doctor` surface | Config & Credential | CLI → config loader → env/keyring sources → redacted display | Keyring unavailable on headless/CI | CLI tests with temporary config homes and mocked credential store; verify redacted output shows token presence without value |
| Task 4 | Sync command populates cache via adapter seam; idempotency via sync_event key; index build triggered on sync completion; projection import stores provenance tag | Cache & Sync Bootstrap, GitCode Adapter, Index | sync CLI → adapter fetch → cache upsert → index build → sync_event insert | Adapter failure mid-sync | Fixture-backed integration test: sync --issues --wiki --index; verify records, sync event, index chunks present; offline get/search/snippet succeed |
| Task 5 | CLI read commands: list, search, get, snippets, backlinks, recent, link-check, stale-index, cache-status, list-chunks, export, diff; all cache-first | CLI Read Surface, Index, Snapshot | CLI → cache/index query → deterministic JSON/Markdown output | Missing-index state unreported | CLI integration test over fixture cache with network disabled; verify each command returns repo-scoped output with stale/missing-index warnings where applicable |
| Task 6 | MCP tools mirror CLI read semantics; `get_snippet`, `recent_changes`, `link_check`, `stale_index_report`, `cache_status`, `search_chunks`, `list_chunks`, `sync_status` exposed | MCP Server, Index | MCP tool call → cache/index query → JSON-RPC response; output matches CLI equivalent byte-for-byte | Tool schema drift from CLI output | MCP JSON-RPC test over fixture cache; compare tool responses against CLI read output for matching queries |
| Task 7 | HTTP/SSE server on 127.0.0.1 bind, stdio mode for single client; `/health`, `/ready`, `/sse`, `/message` endpoints; correlation IDs via X-Request-ID | MCP Server, Cache Concurrency | HTTP/SSE transport → cache reader → concurrent client reads; stdio stdin/stdout for local mode | SSE reconnection and client tracking | Runtime test: start HTTP/SSE server, connect two clients, execute concurrent reads; verify both succeed, health/ready return 200, correlation IDs in logs |
| Task 8 | SQLite WAL mode with busy timeout; per-process writer lock (one sync/write at a time); unlimited concurrent readers; migration blocked under lock conflict | Cache Concurrency | WAL writer → exclusive lock → sync/index; readers → shared WAL readers; migration → lock check → retry/error | WAL file growth under sustained read load | Go concurrency test: shared cache, two concurrent readers + one writer; verify readers succeed, writer gets busy error, migration blocked |
| Task 9 | Write commands with `--dry-run` (no mutation) and `--live` (adapter call → audit row → cache refresh); idempotency via source fingerprint; conflict detection; no adapter = no success | GitCode Adapter, CLI Write Surface | write CLI → validate → dry-run OR adapter call → audit_trail insert → cache upsert | Partial success after adapter call but before audit write | Mock adapter test: dry-run yields no mutation; live with unavailable adapter returns typed error; successful write produces audit row and cache update |
| Task 10 | records.provenance column: `remote` (canonical), `projection` (local import), `bridge` (markdown export re-import); remote truth never overridden by projection sync; projection-only aliases not promoted to remote identity | Cache & Sync Bootstrap | import CLI → projection provenance tag; sync CLI → remote provenance tag; identity resolution prefers remote over projection for same canonical id | Ambiguous projection aliases | CLI test: import projection, then sync remote; verify remote records tagged as canonical, projection provenance preserved, projection-only alias not resolved as remote |
| Task 11 | snapshots and snapshot_chunks tables; export-snapshot emits chunks+citations or missing-index warning; diff-snapshot resolves stored IDs and rejects unknown with not-found | Snapshot & Diff, Index | index → snapshot→snapshot_chunks insert; export → snapshot lookup → chunk emission; diff → two snapshot id lookups → comparison | Expired/missing snapshot ids | CLI test: index, export-snapshot via valid ID, diff-snapshot with valid IDs, diff-snapshot with unknown ID returns not-found; verify export warns on missing index |
| Task 12 | Default heading-delimited chunker; max-section fallback at paragraph/line boundaries; sliding-window chunker as optional policy; chunk schema: source range, content hash, repo_id, record_id, snapshot_id | Index & Chunking | index CLI → policy selection → chunk generation → chunk table insert; CLI/MCP query → chunk table → result with source ranges | Sliding-window boundary alignment with citation ranges | Local indexing test: index fixture docs with both policies; verify determinism, max-size compliance, source range validity; CLI/MCP read returns chunk data |
| Task 13 | Sanitized fixture adapter for offline tests; live adapter gated by `GITCODE_LIVE_TEST=1` env + token; responses redacted before durable artifact write | GitCode Adapter, Docs & Dogfood | fixture tests → fixture adapter → no network; live tests → env check → live adapter → redacted response capture | Accidental secret capture in redaction | Local test: `go test ./...` passes offline with fixtures; `GITCODE_LIVE_TEST=1 go test -run Live` skips or runs with redacted output |
| Task 14 | Docs under `docs/`: install, config-reference, repo-binding, secrets, mcp-setup, read-walkthrough, write-walkthrough, troubleshooting, fixture-capture, dogfood-checklist | Docs & Dogfood Feedback | Reader follows doc steps → commands succeed or return documented diagnostic | Doc drift from actual CLI behavior | Doc smoke test: run each documented command against fixture cache; verify output matches documented expectations |
| Task 15 | Day-slice ordering: Day1=config/repo bind, Day2=fixture sync/index, Day3=CLI reads, Day4=MCP parity+transport, Day5=concurrency+write safety, Day6=snapshot integrity, Day7=docs+live validation+dogfood feedback | All subsystems | Each day's work produces an executable product check; final day verifies offline CLI and MCP reads for one issue and one wiki page | Task dependency chain breaks mid-week | Checklist: for each day, run the target command(s) against fixture cache; verify each day-slice gate passes before proceeding |
| Task 16 | Dogfood feedback artifact under `project/dogfood/feedback.md` with lint rules preventing secret/path leakage; validator command `check-dogfood-feedback` | Docs & Dogfood Feedback | Coordinator records friction observations, missing metadata, design-agent prompt improvements → artifact written → lint check passes | Accidental private data in feedback | Run lint/check command; verify zero findings for secrets, private paths, tracker/wiki names |

### Risks And Validation
- SQLite WAL file growth under sustained MCP read load — mitigation: periodic WAL checkpoint on sync completion and optional checkpoint on server shutdown; severity: low
- Adapter seam abstraction leaks GitCode-specific behavior — mitigation: sanitized fixture capture of live responses; adapter interface defined by domain operations, not HTTP details; severity: medium
- Chunking policy mismatch with real GitCode wiki formatting — mitigation: sliding-window policy as fallback; fixture capture includes real wiki pages; severity: medium
- HTTP/SSE multi-client state drift when one client triggers sync — mitigation: sync is explicit CLI-only, never triggered by MCP client; read-only MCP tool set prevents accidental mutation; severity: low
- Public-safety breach from misconfigured fixture capture — mitigation: fixtures live under `testdata/` with redaction CI check; live-capture command enforces redaction before write; severity: medium

- `doctor --runtime-audit --repo <repo_id>` CLI test against fixture cache — verifies version, config path, repo binding, cache row counts, MCP tool list, sync/index readiness, and failure classes are reported (proves outcome-1, task-1).
- Two-repo fixture integration test: add repos with overlapping aliases, run `get` by alias — verifies scoping or collision rejection (proves outcome-2, task-2).
- CLI test with temporary config home and mocked credential store: `config init`, `config locate`, `config show --redacted`, `auth status` — verifies active path, precedence, token presence without value (proves outcome-3, task-3).
- Fixture-backed `sync --repo <repo_id> --issues --wiki --index` followed by offline `get`, `search`, `get-snippet` — verifies cache populated, index built, offline reads succeed (proves outcome-4, task-4).
- Offline CLI read command suite over fixture cache — verifies each command returns repo-scoped deterministic output with stale/missing-index warnings (proves outcome-5, task-5).
- MCP JSON-RPC test over fixture cache: invoke each tool and compare response against equivalent CLI read — verifies byte-equivalent parity (proves outcome-6, task-6).
- Runtime transport test: start HTTP/SSE server, two MCP clients connect and read concurrently, verify `GET /health` and `GET /ready` return 200, request logs include correlation IDs (proves outcome-7, task-7).
- Go concurrency test with shared SQLite cache: two concurrent readers + one writer — verifies readers succeed, writer gets busy/owned error, migration blocked (proves outcome-8, task-8).
- Mock adapter write test: `write issue create --dry-run` (no mutation), `--live` with error-injecting mock (typed error, no audit row), `--live` with success mock (audit row + cache refresh) (proves outcome-9, task-9).
- CLI import-then-sync test: import projection, sync remote, verify provenance tags and identity resolution (proves outcome-10, task-10).
- CLI snapshot test: index, export-snapshot via valid ID (chunks+citations), export without index (warning), diff-snapshot with valid IDs, diff-snapshot with unknown ID (not-found) (proves outcome-11, task-11).
- Local index+read test with both heading-delimited and sliding-window policies — verifies determinism, source ranges, max-size compliance, CLI/MCP queryability (proves outcome-12, task-12).
- `go test ./...` without credentials (offline pass), `GITCODE_LIVE_TEST=1 go test -run Live` without token (skip), with token (run with redaction) (proves outcome-13, task-13).
- Doc smoke test: execute each documented command against fixture cache and verify output matches expectations (proves outcome-14, task-14).
- Day-slice checklist execution: verify each day's product check produces expected result; final day verifies offline CLI+MCP reads (proves outcome-15, task-15).
- `check-dogfood-feedback` validator run against feedback artifact — verifies zero secrets, private paths, tracker/wiki names (proves outcome-16, task-16).

## Benefits
- Eliminates network dependency for every routine agent read; after a single sync, all CLI and MCP reads work offline.
- Shared SQLite cache serves multiple MCP clients concurrently via HTTP/SSE, reducing duplicate sync overhead.
- Deterministic chunking and snapshot exports enable reviewable, diffable artifact generation for CI/audit pipelines.
- Idempotent write paths with audit trails prevent silent data loss without requiring a full platform migration.
- Public-safe by construction: token stored only in OS credential store, fixtures sanitized, live validation opt-in.

## Competition / Alternatives
- `mcp` — The Model Context Protocol specifies JSON-RPC over stdio and SSE, which this architecture directly adopts for MCP tool surfaces; we extend with shared-cache multi-client HTTP/SSE serving.
- `gh-cli` — GitHub CLI provides repo-scoped issue/wiki reads and writes with credential sourcing; this architecture matches the repo-scoping and credential-store pattern while adding offline cache-first semantics.
- `git-credential-manager` — GCM sources credentials from platform-native stores with env fallback; our config_credential subsystem adopts the same layered sourcing pattern with redacted display and doctor diagnostics.
- `go-keyring` / `keyring` — Go keyring libraries provide cross-platform credential-store access (macOS Keychain, D-Bus, WinCred); this architecture wraps them for token retrieval with graceful degraded-to-env fallback for CI.
