# Architecture

## Goal

Provide a cache-first tooling layer that lets AI agents and humans search, inspect, link, export, and eventually synchronize GitCode tracker/wiki data even when live GitCode access is slow, flaky, or unavailable.

## Non-Goals

- Do not require live GitCode network access for routine reads.
- Do not make remote issue ids replace stable source ids such as `DOC-123`.
- Do not hide remote GitCode mutations behind background maintenance; daemon sync is remote-read/local-cache-write work for explicitly enrolled caches only.

## Components

| Component | Purpose |
| --- | --- |
| Source ingest | Read markdown, tracker, or wiki exports and extract source/task/page metadata. |
| Local cache | Store normalized records, full text, backlinks, identity map, remote metadata, sync status, and conflicts. |
| Repository documentation retrieval | Resolve committed corpus policy, scan exact local Git blobs, maintain metadata-only revision sets/vectors, and hydrate digest-verified citations without fetching. |
| Link resolver | Resolve legacy ids, local paths, wiki pages, and remote issue/page ids. |
| GitCode adapter (fixture + live providers) | Encapsulate fixture/offline records and live tracker/wiki API calls, pagination, auth, rate limits, attachments, and write semantics. |
| CLI | Provide explicit commands for sync, search, get, link-check, export, diff, and diagnostics. |
| MCP server | Expose cache-first reads plus explicit live lifecycle tools for sync, index, diagnostics, and audited issue/PR writes. |
| Local coordinator | Maintain an explicit registry of cache identities and schedule bounded head refresh, tail backfill, and RAG repair without putting network work on read paths. |
| Embedded admin UI | Serve a session-protected SvelteKit operator console from the coordinator's loopback-only HTTP listener. |
| Export snapshots | Produce deterministic markdown/JSON/SQLite snapshots for review, rollback, and audit. |

See [Component Architecture](component-architecture.md) for the durable component catalog, runtime flow, and boundary rules distilled from the historical design-package material.

## Provider Selection

Provider mode is resolved once at command start and does not switch while the command is running.

- `auto`: default mode. Cache read commands stay cache-first; GitCode-touching lifecycle commands select the live provider.
- `live`: selected for lifecycle commands when credentials resolve. `--live` remains accepted as a compatibility alias.
- `offline-fixture`: selected by explicit `--offline` or `--fixture`, and by write `--dry-run`. It uses deterministic fixture/offline providers for docs smoke and tests.
- `unavailable`: selected when a GitCode-touching command needs live credentials but none are available. The command fails with a credential diagnostic instead of silently falling back to fixtures.

Selection predicate:

| Predicate | Provider mode |
| --- | --- |
| cache read command | cache-first local service |
| lifecycle command plus credential | `live` |
| lifecycle command plus no credential | `unavailable` |
| `--offline` or `--fixture` | `offline-fixture` |
| write command plus `--dry-run` | `offline-fixture` validation |

## Credential Pipeline

Credentials are resolved in priority order:

1. `GITCODE_TOKEN` environment variable.
2. Keychain source when available. Native macOS Keychain support is optional, build-tag/platform gated, and no-ops on unsupported builds.
3. None. Live commands report auth/provider-unavailable diagnostics when no token is available.

`gitcode-mcp auth status` reports the credential source and a redacted token preview only. Tokens, raw `Authorization` headers, private repository coordinates, cookies, and raw API response bodies must not appear in CLI output, MCP responses, logs, fixtures, or test snapshots.

## Data Flow

```text
source markdown / tracker export / wiki export
        |
        v
source ingest -> local cache -> CLI / MCP reads
        |              |
        |              v
        |        export snapshots
        v
GitCode adapter <-> tracker/wiki remote state
```

Writes flow through explicit CLI or MCP live-write commands, require idempotency keys or deterministic write fingerprints, call the live GitCode adapter for provider confirmation, and then record audit/cache evidence. Issue updates add a durable pre-mutation fence: the adapter reads the canonical preimage, the service atomically claims the key with hashed preimage invariants, and only then may the PATCH start. Ambiguous PATCH/readback outcomes remain fenced until canonical readback proves the requested state; they are never replayed as a second blind PATCH. Routine reads continue to flow through the local cache and never trigger background writes.

Repository documentation follows a separate local-authority path:

```text
committed .gitcode/gitcode-mcp.yaml + exact Git tree
        |                              |
        v                              v
  corpus policy              bounded blob hydration
        |                              |
        +--> metadata/vector revision set --> CLI / MCP / Admin status
                                       |
                                       +--> digest-verified citations
```

Git is the only durable document store. SQLite never persists repository
document or chunk text. See [Repository documentation RAG](repository-documentation-rag.md).

## Repo-Local Cache Storage

The cache resolver supports two storage modes:

- `global`: the compatibility default. The cache lives in the OS/user cache directory or an explicit configured path.
- `repo-local`: an opt-in mode that keeps the SQLite cache next to the current Git worktree under `.gitcode/mcp/cache.db`.

Repo-local layout:

```text
<git-worktree>/
  .gitcode/
    gitcode-mcp.yaml
    mcp/
      cache.db
      cache.db.lock
      exports/
      snapshots/
```

The tracked config file is `.gitcode/gitcode-mcp.yaml`; generated cache state under `.gitcode/mcp/` should be ignored. This makes repository intent reviewable without committing SQLite databases, locks, exports, or snapshots.

Cache selection is resolved once at process startup:

```mermaid
flowchart TD
  A["CLI or MCP startup"] --> B{"--cache-path?"}
  B -->|yes| C["Use explicit command cache path"]
  B -->|no| D{"GITCODE_MCP_CACHE_DIR?"}
  D -->|yes| E["Use env cache directory"]
  D -->|no| F{"User/global cache_path?"}
  F -->|yes| G["Use configured global cache path"]
  F -->|no| H{"cache_mode: repo-local?"}
  H -->|yes| I["Walk cwd upward to Git root"]
  I --> J{"Git root found?"}
  J -->|yes| K["Use <root>/.gitcode/mcp/cache.db"]
  J -->|no| L["Use global default"]
  H -->|no| L
```

Repo-local discovery reads `.gitcode/gitcode-mcp.yaml` from the discovered Git root. A user-level config may also set `cache_mode: repo-local`; the worktree still supplies the concrete root. Explicit cache paths and `GITCODE_MCP_CACHE_DIR` always win, which gives migrations and emergency diagnostics a stable escape hatch.

Migration is intentionally non-destructive. Existing global caches remain the default. Teams can opt into repo-local mode per worktree, then run normal `sync`, `index`, and `doctor` commands to populate and verify the new cache. No automatic copying from the global cache happens during startup; operators who want migration can export/sync again or use future explicit cache migration tooling.
