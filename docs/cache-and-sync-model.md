# Cache And Sync Model

## Local Cache Requirements

The cache should answer routine agent queries without network access:

- search issues, pull requests, comments, source notes, and wiki-like pages;
- get a record by legacy id, path, remote id, or URL;
- resolve backlinks;
- explain sync status and conflicts;
- export deterministic snapshots;
- compare local/export/remote state when remote data is available.

## Candidate Storage

Start with SQLite plus optional deterministic markdown/JSON exports.

Candidate tables:

- `records`: normalized issues, pull requests, comments, pages, and sources.
- `full_text`: search text and extracted headings.
- `identity_map`: legacy id, local path, remote id, remote URL, aliases.
- `links`: source record, target record, link kind, raw link text, resolved target.
- `remote_revisions`: remote revision metadata and fetch timestamps.
- `sync_events`: sync command, idempotency key, result, error class, evidence path.
- `conflicts`: local value, remote value, conflict class, resolution state.

## Sync Principles

- Reads are cache-first.
- Writes are explicit commands.
- Every remote write has an idempotency key or deterministic replay guard.
- Every sync creates reviewable evidence.
- Remote ids are aliases. Legacy ids remain stable migration keys.
- Failed writes stay visible until retried or deliberately dismissed.

## Concurrent Cache Access

The cache is optimized for agent-side fan-out reads. Routine read operations such as `list`, `get`, `search`, status, export, diff, and MCP read tools must not require the process-wide writer lock when the SQLite schema is already current.

The writer lock is reserved for operations that mutate cache state or need exclusive migration admission:

- schema initialization and supported schema upgrades;
- sync and index refresh operations;
- explicit write commands and cache-maintenance commands that persist state.

Opening a current-schema SQLite cache may check schema compatibility, but it must not acquire the migration writer lock just to prove that no migration is required. This keeps parallel CLI/MCP reads from failing before they reach SQLite. SQLite WAL mode remains the storage-level concurrency mechanism for readers while a logical writer lease exists.

When a real writer conflict remains, the caller should receive a typed cache-busy diagnostic (`cache_busy` or `cache_lock_contention`, depending on the surface) with holder metadata when available, not a generic `internal_error`.

## Live Sync Semantics

`gitcode-mcp sync` keeps the fixture/offline provider as the default. `gitcode-mcp sync --live` opts into the live GitCode provider for the configured repository and uses the current cache as the durable local source for later reads.

The sync command supports these live sync selectors:

- `--repo REPO` selects the configured repository binding.
- `--issues` bulk-syncs issue records.
- `--wiki` bulk-syncs wiki records.
- `--pulls` bulk-syncs pull request records.
- `--comments` bulk-syncs pull request comments for cached pull request records.
- `--id ID` and `--input ALIAS` sync one stable record or remote alias.
- `--index` builds the local index after sync.
- `--idempotency-key KEY` supplies a deterministic sync event key.
- `--max-pages`, `--max-records`, and `--per-page` bound collection sync when the selected surface supports collection bounds.

Bulk sync treats issues, wiki pages, pull requests, and pull request comments as bounded collections. Issue and pull request sync page through list APIs and commit each returned record independently. Issue sync uses list-level issue revision metadata before deciding whether issue comments need to be listed again. Wiki sync passes record bounds into the wiki provider traversal before committing individual pages, then uses list-level wiki revision metadata before deciding whether a page body fetch is necessary. Pull request comment sync walks cached pull request records and applies record bounds across the resulting comment records. Issue sync covers issue data and comments as part of the source graph for that issue; wiki sync covers wiki page data. Live adapter route construction stays behind the provider boundary, and operator docs should use sanitized placeholders rather than real repository coordinates.

The command context carries the configured `default_timeout`, including the `--timeout` override, so large collection syncs have a whole-operation deadline in addition to provider-level request timeouts. When the deadline or caller cancellation fires, completed resource commits remain visible in cache and the sync response reports partial counts plus a typed diagnostic such as `sync_timeout` or `sync_cancelled`.

Labels and milestones are not yet exposed as bulk sync service surfaces. When those collection surfaces are added, they should use the same `SyncBounds` and partial-result contract.

Each successful resource sync records a `SyncEvent` with:

- `started_at` and `completed_at` timestamps;
- `remote_revision` when the provider exposes one;
- count metadata for fetched, inserted, updated, skipped, and conflict totals;
- collection metadata counts when available: `listed`, `fetched_detail`, `skipped_by_revision`, and `failed`;
- `zero_delta` when a re-sync fetched records but all fetched content was unchanged.

Re-syncing unchanged content records a zero-delta event instead of duplicating cached records. `gitcode-mcp sync_status` reports cache freshness from the stored source records and latest completed sync events; the command help describes the query surface, while the model above defines the event fields persisted by sync.

## Metadata-First Collection Sync

Collection sync should do cheap list work before expensive detail or body fetches. Bounds are applied to list candidates first, then the sync engine uses collection-specific metadata to decide whether each bounded candidate needs a detail/body request.

Current collection behavior:

| Collection | List-level marker | Current sync strategy |
| --- | --- | --- |
| Wiki pages | `sha`/`revision` from wiki contents/list entries | Cache-aware. If cached `remote_revision` matches the list marker, cached source content exists, and status is fresh, sync records a zero-delta `skipped_by_revision` result without fetching the page body. New, changed, incomplete, or marker-less records fetch the full page body. |
| Issues | `updated_at`, `comments`, stable `id`, numeric `number`, and the list-provided source content | Cache-aware for the current expensive child read. Bulk issue sync stages issue content from the list payload and does not perform per-issue detail fetches. If cached `remote_revision` matches the list marker, sync skips the per-issue comments list call and records `skipped_by_revision`. New or changed issue markers list comments again. |
| Pull requests / merge requests | `updated_at`, stable `id`, numeric `number`, branches/diff refs, labels, and list-provided source content | Bulk pull request sync stages from the list payload and does not perform per-PR detail fetches in the current path. The stored `remote_revision` is the list-version token so future detail expansion can compare before adding detail calls. |
| Pull request review comments | Comment list payloads include stable ids, discussion ids, and `updated_at` timestamps | Comment sync stages from list-comment payloads. It still needs the parent PR comment list call because there is no persisted parent comment-collection checkpoint; individual comment revisions are stored after the list is fetched. |
| Issue comments | Issue list `updated_at` plus `comments` count, with comment `updated_at` available after listing | Not exposed as an independent bulk selector. As part of issue sync, unchanged issue revision metadata skips the issue comment-list call; changed issue metadata refreshes comments. |
| Labels | No reliable update marker documented for this cache surface | Not a first-class bulk sync collection yet; use full refresh or a future invalidation strategy. |
| Milestones | Model supports `updated_at`, but list behavior and cache surface need verification | Not a first-class bulk sync collection yet; do not claim metadata skip until live discovery confirms the marker and persistence contract. |

The compatibility counters keep their older meaning: `fetched` counts one processed remote candidate and `skipped` counts unchanged work. Metadata-first sync adds `listed`, `fetched_detail`, and `skipped_by_revision` so callers can distinguish "listed and skipped without body fetch" from "fetched detail and found no content delta."

## Partial Failure Handling

Bulk sync treats each listed issue or wiki page as an independent resource. A failure for one resource does not roll back resources that already synced successfully and does not prevent later resources from being attempted.

When any resource fails, the service returns `PartialSyncError` with:

- `success_count` for resources committed successfully;
- `failure_count` for resources that failed;
- per-resource details including source id or remote alias, remote type, and diagnostic message.

Successful resources remain committed to the cache. Failed resources are reported to the caller and can be retried with the same repository, selector, and idempotency key strategy.

Actionable failure classes include:

- authentication or authorization failures from the live provider;
- rate-limit responses;
- network, timeout, and context-cancellation failures;
- partial or oversized provider responses;
- missing remote resources;
- cache integrity, write, or lock failures.

The CLI renders the aggregate success and failure counts and resource details. Diagnostics must stay public-safe: tokens, Authorization headers, cookies, private repository coordinates, and raw API bodies are not printed.

## Cache Migration

The implemented cache schema version is `11`, matching `currentSchemaVersion` in `internal/cache/schema.go`.

The primary version source is the SQLite `schema_version` table. Migrations also update `PRAGMA user_version` as an additive SQLite diagnostic bridge, but cache compatibility decisions use `schema_version`.

Compatibility policy:

| Detected version | Behavior | Operator action |
| --- | --- | --- |
| New empty cache | Initialize normally at schema version 11 | None |
| 11 | Open normally; reads and writes are allowed | None |
| 2-10 | Open read-compatible but writes are blocked until migration | Run `gitcode-mcp migrate-cache --confirm` |
| 1 | Block migration as pre-supported/iteration-1-equivalent | Reinitialize with `gitcode-mcp reinit-cache` or delete the cache and re-sync |
| 0, missing, or empty `schema_version` in a non-empty cache | Block as pre-schema-versioning or unknown | Reinitialize with `gitcode-mcp reinit-cache` or delete the cache and re-sync |
| Greater than 11 | Block as newer than this binary supports | Upgrade `gitcode-mcp` to a binary that supports the schema |

`gitcode-mcp migrate-cache --confirm` runs supported older-version migrations in place from the detected version to version 11. It creates a backup at `{cache-path}.backup-{timestamp}` before applying changes. Each migration step runs in a transaction and advances both `schema_version` and `PRAGMA user_version` only after that step succeeds.

Opening an older compatible cache without migration is read-compatible but write-blocked so operators can inspect the cache and run diagnostics before applying the migration. New caches are initialized directly at the current schema version.
