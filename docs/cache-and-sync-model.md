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
- `embedding_namespaces`, `chunk_embeddings`, `rag_index_runs`, and `rag_coverage_state`: model-scoped RAG coverage, content-generation coverage, and indexing progress state.
- `repo_doc_revision_sets`, `repo_doc_chunks`, `repo_doc_membership`, and `repo_doc_vectors`: metadata-only repository-document revision identity, byte/line locators, Git/worktree authority, and reusable vectors. These tables never contain source or chunk text.
- `cache_identity`, `repo_content_state`, and `maintenance_frontiers`: durable cache identity, hash-driven invalidation generation, and independent daemon head/tail coverage.

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

When a real writer conflict remains, the caller should receive a typed cache-busy diagnostic (`cache_busy` or `cache_lock_contention`, depending on the surface) with holder metadata when available, not a generic `internal_error`. Public diagnostics expose an opaque `cache_ref`, operation, repository, start time, and process id; absolute lock/cache paths, DSNs, query parameters, fragments, arbitrary lock-file hints, and invalid legacy owner identifiers stay internal. New lock-owner records use the durable cache UUID for correlation when the current schema provides it and do not persist the cache path; legacy owner metadata is read only for compatibility and passes a bounded identifier allowlist before projection.

Foreground bulk sync takes one logical writer lease for the selected cache and collection before making provider requests. Admission failure is operation-level: it returns one typed contention error with no provider traversal, cache mutation, or per-record partial-failure flood. Composite all-sync reuses its outer lease for nested collection work. The lease marker is private and scoped to the exact cache lock path plus repository id; a different cache path has an independent writer lease and can synchronize concurrently.

## Live Sync Semantics

`gitcode-mcp sync` uses the live GitCode provider by default for a configured repository and uses the current cache as the durable local source for later reads. `gitcode-mcp sync --offline` or `gitcode-mcp sync --fixture` selects the deterministic fixture/offline provider for docs smoke and tests.

Large collection sync can run through the local service job queue:

```sh
gitcode-mcp sync --repo YOUR_OWNER/YOUR_REPO --issues --pulls --pr-comments --daemon
gitcode-mcp sync --repo YOUR_OWNER/YOUR_REPO --issues --pulls --pr-comments --detach
gitcode-mcp service jobs
gitcode-mcp service attach JOB_ID
gitcode-mcp service cancel JOB_ID
```

`--daemon` starts a service-owned sync job and keeps the CLI attached to compact sync progress. `--detach` starts the same service-owned job and returns the job id immediately. The daemon path is collection-oriented; targeted `--id`/`--input` sync remains a foreground operation. Existing frontiers/checkpoints still drive resumability after bounded or interrupted collection sync.

Every daemon selector combination, including the default issues+wiki selection,
uses a collection-level `fetching → staged → waiting_commit/retrying → committing
→ committed|rejected|superseded` protocol. Provider requests finish before the
checksummed stage is admitted to the cache writer queue. A contended commit
replays the persisted normalized response and never repeats provider traffic.
Stages are bound to cache UUID, schema, an exact fingerprint of the remote
repository route (`owner`, `name`, API base, and scopes), registration,
collection checkpoint, provider revision, and idempotency key; recovery rejects
a changed or missing target before opening it writable. An omitted daemon bound
means one provider page (normally at most 100 list items) per durable stage,
rather than an unbounded traversal. Caller bounds can tighten that chunk but
cannot enlarge it past one provider page, 10,000 produced records, the provider
response ceiling, or the 16 MiB stage envelope. The daemon lowers its individual
HTTP response ceiling to 7.5 MiB and its normalized payload ceiling to 15.5 MiB,
leaving explicit space for the staged JSON envelope. Wiki traversal applies the
same record-page clamp and a cumulative serialized-page byte budget while it
fetches bodies; a recursive tree cannot escape the durable batch boundary.
When the next wiki document would cross that budget, the already bounded prefix
is committed and the maintenance frontier records an exact record offset. The
next run resumes at that offset; only a single document that cannot fit by
itself is terminally rejected.
Maintenance keeps that wiki offset separate from issue, comment, and pull
request provider-page checkpoints. A mixed/default sync persists its ordered
collection workflow with every stage. The newest committed stage remains the
restart checkpoint until the next collection is durably staged or the terminal
job snapshot is saved. Recovery selects the furthest staged collection, treats
a receipt as proof for that collection only, and continues the remaining
suffix without refetching any committed prefix. Every workflow checkpoint also
stores a content-free aggregate outcome (counts plus sanitized failure class
and collection), so a partially fetched but successfully committed prefix
cannot become a false success after restart. Each selected collection still
receives its own cursor; already-fresh collections are omitted instead of being
replayed because another collection needs work.

Provider retries are isolated by collection and frontier. A transient timeout,
rate limit, or provider 5xx schedules only that collection with bounded,
jittered backoff; other due collections continue immediately and publish their
successful cache transactions. Authentication, permission, query, schema, and
data-validation failures are terminal for the affected collection. Public job
state exposes a content-free aggregate health plus per-collection outcome,
opaque frontier reference, counts, retry budget, next attempt, and last success
time. The private service journal persists only retry authority and request
selectors—never fetched response bodies—and lets a restarted daemon resume the
pending collection without replaying successful siblings. CLI, MCP, and the
admin Jobs view project the same state. The admin UI additionally supports a
URL-backed aggregate-health filter and an explicit action that retries one
failed collection while retaining the others.

Collection retry checkpoints are bounded to 256 entries, 1 MiB of private
metadata, and 24 hours. Recovery reconciles an exact staged transaction or
SQLite commit receipt before applying age-based checkpoint cleanup, so durable
cache truth cannot be downgraded to an expired retry. Admin retry actions first
persist a hashed, intent-bound `pending` receipt. Replaying that intent after a
restart first resolves an exact opaque action-intent reference that is attached
atomically to whichever current maintenance job was created, resumed, or
coalesced, together with that admission disposition. Jobs carrying an
unresolved reference are protected from history pruning, while the private
correlation metadata is excluded from public job JSON. If no job was admitted
before the crash, the private receipt's minimal registration authority remains
sufficient to re-drive reconciliation even after the source job expires;
daemon startup performs that recovery without requiring the browser to retain
the original idempotency key. Settlement atomically replaces the pending
receipt with a terminal receipt and durably releases the job-retention pin. A
release write failure keeps both sides recoverable and rejects further receipt
admission when the bounded journal cannot safely evict them.
Pending intents are never evicted by settled-receipt retention. If unresolved
intents fill the bounded journal, admission fails closed before starting a new
mutation and returns a typed remediation diagnostic.

Comment fan-out is checked as produced records and serialized bytes before it
can accumulate across parents.
Per-stage limits are enforced together with a 64 MiB/50,000-record/256-stage
aggregate runtime budget. Committed predecessor stages leave capacity after a
successor is durable; the one newest workflow checkpoint is removed when the
job becomes terminal and otherwise expires at the stage age limit. That retained
committed checkpoint, like cancelled/rejected evidence, continues consuming all
aggregate quotas while it exists. Checkpoint deletion is ordered after a
successful durable terminal `jobs.json` write; a snapshot write failure leaves
the checkpoint recoverable. Issues, issue comments, wiki, pull requests, and
pull request comments all use this daemon protocol. Foreground sync retains its
synchronous compatibility behavior.

Each SQLite publication transaction includes normalized graphs, collection and
maintenance frontier/checkpoint updates, and a checksum-bound sync commit
receipt. The receipt is the authority if the process commits SQLite but cannot
atomically rename the terminal journal update (for example, ENOSPC): restart
reports the job as committed without provider refetch or a false rejection.
Optional post-commit queue-summary reads cannot downgrade that committed state.
Provider fetch admission is allowed while another cache writer is active, and a
writer arriving during provider fetch is admitted because sync has not reserved
the writer lane. Only the short commit phase joins the per-cache FIFO and
bounded contention backoff. The external-writer comparison and transition to
the public `committing` lease happen under one job-manager lock, so a direct,
RAG, or repository-document writer cannot enter between the check and commit
reservation. Recovery uses the same atomic admission path.

Service job state is stored separately from the cache in the mode-`0600` service runtime `jobs.json` snapshot. It is operational state, not cache content. Active jobs (`queued`, `running`, and the short durable `cancelling` transition) have no TTL and remain visible as active work in the Admin UI. By default, succeeded/superseded jobs expire after 48 hours; failed/interrupted/cancelled jobs expire after 14 days. The latest significant failure per maintenance registration or work stream survives its ordinary TTL inside a separately bounded diagnostic cohort. A final 128-terminal-job cap and 256-progress-event cap keep the snapshot bounded. Pruning runs on load, job updates/completion, and idle maintenance reconciliation. It never deletes cached GitCode records, sync frontiers, maintenance policy, RAG indexes, or audit receipts.

The sync command supports these live sync selectors:

- `--repo REPO` selects the configured repository binding.
- `--issues` bulk-syncs primary issue records and durably enqueues secondary comment coverage without calling per-issue comment endpoints.
- `--wiki` bulk-syncs wiki records.
- `--pulls` bulk-syncs pull request records.
- `--issue-comments` drains the durable issue-comment queue independently of parent issue traversal. With a complete issue frontier it uses the repository-wide comment collection; otherwise it keeps the per-issue compatibility path. Combine it with `--issues` to run parent backfill first and then drain comments.
- `--pr-comments` bulk-syncs pull request comments and review metadata for cached pull request records. With `--input pr:N`, it calls the per-PR comments adapter exactly once and does not enumerate other cached PRs.
- `--comments` is a compatibility selector for `--pr-comments` in bulk sync; `--input issue:N` routes to issue comments and `--input pr:N` routes to the same targeted PR-comment path.
- `--id ID` and `--input ALIAS` sync exactly one stable record or remote alias. A matching surface selector such as `--issues --input issue:42` is accepted as a type assertion and still uses the single-record adapter path; mismatched or multiple collection selectors are rejected before any provider or cache access.
- `--index` builds the local index after sync.
- `--idempotency-key KEY` supplies a deterministic sync event key.
- `--max-pages`, `--max-records`, and `--per-page` bound collection sync when the selected surface supports collection bounds. They are rejected with `--id` or `--input` because an exact read has no pagination. Foreground collection sync without a max bound traverses until `end_of_collection` or a complete frontier watermark proves the remaining tail is already cache-covered; daemon sync applies the durable one-page/100-record staging chunk described above.

The MCP `sync_live` surface also exposes explicit `issue_comments` and
`pr_comments` selectors. Its legacy `comments` selector resolves from the
selected parent collection: `issues + comments` drains issue comments after
the parent issue sync, while `pulls + comments` syncs pull request comments.
Using `comments` with both parent kinds is rejected before sync; callers that
need both surfaces must select `issue_comments` and `pr_comments` explicitly.
For compatibility, `comments` without a parent collection still means pull
request comments.

`sync_live` applies the same exact-target rules to `remote_alias`: one matching
parent selector is allowed, collection bounds and mismatched selectors are
rejected before service invocation, and the single-record path never falls
through to collection listing.

Bulk sync treats issues, wiki pages, pull requests, and pull request comments as bounded collections. Foreground compatibility paths may publish records incrementally; daemon jobs publish each provider-complete collection stage in one SQLite transaction, including issue-comment replacement/reconciliation and PR review metadata. Bounded issue and pull request sync request recent-update descending order and record collection frontier metadata in `sync_frontiers`. A bounded, timed-out, or partially failed run only proves that the current invocation traversed a slice; it must not poison later traversal by causing early-stop before older records are backfilled. A later run can use a high-watermark stop condition only when the previous frontier for the same repo, surface, ordering, and filter scope is `complete`. Issue collection sync is parent-first: it persists list-provided issue records and updates `issue_comment_sync`, but never calls a per-issue comment endpoint. With a complete parent frontier, foreground issue-comment sync pages through the repository-wide collection, upserts each page idempotently, reconciles every comment through both provider issue id and issue number, and marks parent queue items complete only after reaching the collection tail. The daemon's durable comment stage uses bounded parent-scoped responses so the complete response for every selected parent can be retried without another network call. Each synchronized comment is also projected as a first-class `issue_comment` source with stable id `ISSUECOMMENT-<issue-number>-<comment-id>`, a parent link to the issue, chunks, full-text search content, and a remote alias. A bounded or interrupted aggregate run leaves the queue pending and restarts from page 1; this conservative replay avoids page-number drift and is cheap because page upserts are idempotent. Only a successful full pass removes stale cached comments and stale issue-comment source projections. Unknown or conflicting parents are explicit retryable reconciliation failures. If the aggregate route is unavailable, or if the parent frontier is incomplete, the service retains the per-issue compatibility path. Wiki sync passes record bounds into the wiki provider traversal before committing individual pages, then uses list-level wiki revision metadata before deciding whether a page body fetch is necessary. Pull request comment sync walks cached pull request records and applies record bounds across the resulting comment records. PR review metadata from the comment payload is stored separately from the searchable comment body so cached reads can group review discussions without live network access. Schema version 13 stores review discussion rows and per-comment diff positions so inline review comments can be matched to changed paths and lines using GitCode position metadata. Schema version 14 stores issue and pull request collection frontier metadata. Schema version 16 adds the durable issue-comment queue. Schema version 17 adds daemon maintenance identity, head/tail frontiers, and content-generation-based RAG coverage. Schema versions 18 and 19 add repository-document revision, chunk, membership, vector, exclusion, and source-registration identity metadata without storing document bodies. Live adapter route construction stays behind the provider boundary, and operator docs should use sanitized placeholders rather than real repository coordinates.

Parent restart behavior is intentionally conservative because the public issue API is page/per-page, not cursor based. If a bounded run cached 5,000 issues while 10,000 remain, the next `--issues` run starts at page 1 because the previous frontier is not complete; resuming directly at a stored page number could skip records when newer issues shift page boundaries. Already cached parent revisions take the cheap `skipped_by_revision` path, do not trigger comment reads, and do not consume the next run's `--max-records`/`--max-pages` coverage budget. The run can therefore scan through the known 5,000 and spend its bound on the missing tail. `pages_listed` and `records_listed` still report actual list traffic, while the per-record `skipped_by_revision` counter explains the rescan. Comment work remains durable and independent in `issue_comment_sync`; `--issue-comments` drains pending/deferred items later and can be bounded with `--max-records` or `--max-pages`. Repository aggregate progress reports `aggregate_requests`, `comments_listed`, reconciliation failures, and `parent_requests_avoided`. A rate limit defers aggregate traversal without fanning out into per-issue requests; the next foreground or daemon run safely replays from page 1.

The command context carries the configured `default_timeout`, including the `--timeout` override, so large collection syncs have a whole-operation deadline in addition to provider-level request timeouts. When the deadline or caller cancellation fires, completed resource commits remain visible in cache and the sync response reports partial counts plus a typed diagnostic such as `sync_timeout` or `sync_cancelled`.

Labels, milestones, and push mirrors are not yet exposed as bulk sync service
surfaces. The `milestones` CLI command and `list_milestones` MCP tool perform a
live list read and refresh cached milestone records. The
`list-push-mirrors` CLI command (`push-mirrors` alias) and
`list_push_remote_mirrors` MCP tool similarly perform an explicit live read and
refresh sanitized `push_remote_mirror` records. `trigger-push-mirror` and
`trigger_push_remote_mirror` use the audited write lifecycle, while
`wait-push-mirror` and `wait_push_remote_mirror` poll sanitized live status
without creating a collection frontier. When milestone or mirror bulk sync is
added, it should use the same `SyncBounds` and partial-result contract.

A mirror trigger atomically claims `in_progress` in the shared SQLite audit
before the provider POST and performs only one transport attempt. Confirmed
triggers become `succeeded`; known safe rejections become `failed`; ambiguous
transport, provider, or readback failures remain `in_progress`, so sequential
or concurrent replay of the same idempotency key cannot issue an unsafe
duplicate operation.

Issue updates use the same durable-claim principle around PATCH. The HTTP
adapter reads a canonical issue preimage, invokes the service claim callback,
and does not attempt PATCH unless that claim succeeds. The audit stores hashes
of the preimage fields and a machine-readable phase, never the duplicated issue
document. Failures known to occur before PATCH, and provider rejections that
prove no mutation, become retryable `failed` entries. Transport uncertainty
after PATCH and all canonical-readback failures remain `in_progress`. A
same-key replay performs canonical GET only: if requested fields match and all
omitted fields still match their preimage hashes, it finalizes as
`recovered_after_ambiguous_write`; otherwise it returns
`write_ambiguous_remote` without a second PATCH. If a safely retryable attempt
observes a changed preimage before its next mutation, it returns the typed
`write_conflict` diagnostic. These records survive service restart in the
shared audit table.

Milestone-aware issue writes also refresh a deterministic `milestone` link from
the cached issue source to the resolved `MILESTONE-<id>` source. The link kind
is replaced atomically for each issue write, so assignment changes do not leave
stale targets and explicit clears remove the cached relationship.

Bulk collection responses also expose traversal metadata when available:

- `pages_listed` and `records_listed` count list-page work done before staging/filtering;
- `skipped_by_watermark` counts list records skipped because a previous `complete` frontier proves that the remaining tail is already cache-covered;
- `ordering` reports the server-side ordering contract, currently `updated_at_desc` for bounded issues and pull requests;
- `stop_reason` reports why traversal stopped, such as `watermark`, `end_of_collection`, `max_pages`, or `max_records`;
- `traversal_status` classifies the run as `complete`, `bounded`, `timeout`, `cancelled`, or `partial`;
- `watermark_status` and `watermark_reason` explain whether early-stop was disabled, eligible, or used. Bounded or partial previous frontiers disable early-stop; complete frontiers make it eligible.

Each successful resource sync records a `SyncEvent` with:

- `started_at` and `completed_at` timestamps;
- `remote_revision` when the provider exposes one;
- count metadata for fetched, inserted, updated, skipped, and conflict totals;
- collection metadata counts when available: `listed`, `fetched_detail`, `skipped_by_revision`, and `failed`;
- `zero_delta` when a re-sync fetched records but all fetched content was unchanged.

Re-syncing unchanged content records a zero-delta event instead of duplicating cached records. CLI bulk sync output is compact by default: stdout reports aggregate success/failure counts, summed sync counters, grouped failure counts, elapsed time, and timeout/cancellation diagnostics, while stderr carries progress lines for the current collection/page and committed record count. Per-resource sync evidence remains available with `--details` or `--records`. `gitcode-mcp sync_status` reports cache freshness from the stored source records and latest completed sync events; aggregate `sync-status --format json` also defaults to a compact summary and uses `--details` or `--records` for the full per-record `results[]` payload.

## Metadata-First Collection Sync

Collection sync should do cheap list work before expensive detail or body fetches. Bounds are applied to list candidates first, then the sync engine uses collection-specific metadata to decide whether each bounded candidate needs a detail/body request.

Current collection behavior:

| Collection | List-level marker | Current sync strategy |
| --- | --- | --- |
| Wiki pages | `sha`/`revision` from wiki contents/list entries | Cache-aware. If cached `remote_revision` matches the list marker, cached source content exists, and status is fresh, sync records a zero-delta `skipped_by_revision` result without fetching the page body. New, changed, incomplete, or marker-less records fetch the full page body. |
| Issues | `updated_at`, `comments`, stable `id`, numeric `number`, and the list-provided source content | Parent-first. Bulk issue sync stages issue content from the list payload, never performs per-issue comment reads, and enqueues comment coverage only when the list marker indicates comments may need refresh. Matching parent revisions use `skipped_by_revision` while pending/deferred child coverage remains independently visible. |
| Pull requests / merge requests | `updated_at`, stable `id`, numeric `number`, branches/diff refs, labels, and list-provided source content | Bulk pull request sync stages from the list payload and does not perform per-PR detail fetches in the current path. The stored `remote_revision` is the list-version token so future detail expansion can compare before adding detail calls. |
| Pull request review comments | The v5 list exposes root comments with nested `reply` records. The read-only v4 discussion route can expose either nested discussions or a flat `data` envelope containing timeline notes and diff positions; v4 reply notes do not reliably carry parent ids. | The adapter flattens v5 nesting to stable parent ids, accepts both observed v4 response shapes, and merges v4 position metadata by comment id. The v5 graph is canonical RAG text; flat v4 rows are retained only when their discussion has inline evidence, so unrelated timeline events do not become chunks. Reply writes refresh the same graph only after v5 list readback. The parent PR comment list call is still required because there is no persisted parent comment-collection checkpoint. |
| Issue comments | Issue list `updated_at` plus `comments` count; the repository aggregate exposes comment `updated_at` and `target.issue.{id,number}`. | `--issues` creates or refreshes durable queue items. Once the parent frontier is complete, `--issue-comments` scans the aggregate route, commits pages idempotently, and reconciles cached parents only after a complete pass. Bounds/interruption leave work pending for page-1 replay. Missing aggregate support or an incomplete parent frontier uses the per-issue fallback. Targeted `--input issue:N` remains an immediate single-record refresh path. |
| Labels | No reliable update marker documented for this cache surface | Not a first-class bulk sync collection yet; use full refresh or a future invalidation strategy. |
| Milestones | Model supports `updated_at`, but list behavior and cache surface need verification | Not a first-class bulk sync collection yet; do not claim metadata skip until live discovery confirms the marker and persistence contract. |
| Push remote mirrors | Stable id plus status/failure/update metadata; no verified collection revision marker | Explicit live list refreshes credential-redacted cache records. No frontier or metadata-skip claim. |

The compatibility counters keep their older meaning: `fetched` counts one processed remote candidate and `skipped` counts unchanged work. Metadata-first sync adds `listed`, `fetched_detail`, and `skipped_by_revision` so callers can distinguish "listed and skipped without body fetch" from "fetched detail and found no content delta."

For large repositories, combine revision metadata with server-side ordering. Bounded issue sync uses `state=all&order_by=updated_at&sort=desc`; bounded pull request sync uses `state=all&order_by=updated_at&direction=desc`. Routine refreshes list the newest changed records first. Cached records whose list revision still matches skip the detail phase, such as issue comments. If the prior `sync_frontiers` row is complete, traversal can stop after the listing falls below that complete high-watermark because the remaining tail is already cache-covered. If the prior frontier is bounded, timed out, or partial, traversal keeps listing past cached rows so older missing records can be backfilled. Full refresh and repair workflows should still be available by using explicit bounds or future full-refresh flags when the operator needs to walk the whole collection.

PR comment collection has no verified repository-wide endpoint. Its safe inclusion policy is therefore explicit and parent-scoped: `--pr-comments` is never implied by `--pulls`, candidates come only from already cached PRs, and large or daemon jobs should supply `--max-records`/`--max-pages`. For review assistance, `--pr-comments --input pr:N` (or MCP `pr_comments=true, remote_alias=pr:N`) selects one already cached PR, performs one provider comments request, and never walks the remaining cache. A durable per-PR queue should use `(repo_id, source_id)` with the list-provided `notes` marker, `pending|deferred|complete` state, attempts, retry-after, last counts, and update time; a rate limit defers the current parent and stops the drain. Until that queue is implemented, interruption restarts the bounded cached-parent walk and idempotent record replacement prevents duplicates. Do not schedule an unbounded full-body PR crawl by default.

RAG sizing must be based on fetched user comment/reply bodies, not a fixed multiplier per PR and not the v4 timeline count. The 2026-07-13 bounded sample described in `gitcode-api-discovery.md` observed 1.00-1.27 size-based 4 KiB chunks per v5 note across five repository strata (1.11 overall). Use that interval only as a conditional planning bound after measuring the selected PRs' v5 note count; it is not a corpus prevalence estimate, and larger unseen bodies leave the theoretical upper bound open.

## Cache Repair And Reset

`gitcode-mcp cache reset --live --repo REPO` is the supported current-schema live repair command. It clears only repo-scoped live GitCode cache surfaces: remote/live records, their searchable source projections, review discussion/position metadata, and `sync_frontiers` for the selected repository. It does not delete the SQLite cache file, repository bindings, fixture/local records for other repositories, global cache directories, or repo-local cache directories outside the selected cache path.

Resetting live cache data also clears the collection frontiers used by watermark early-stop. The next issue or pull request sync therefore cannot trust an old complete frontier and must list from the newest page again. Unchanged records can still skip detail work after they are listed and their revision metadata matches; missing older tail records are backfilled by continuing traversal until the new run reaches its bounds or records a fresh complete frontier. If the reset is part of a repair, run sync without `--max-pages`/`--max-records` to walk to the collection tail, or set explicit bounds large enough to cover the suspected gap.

A bounded or cancelled run records a non-complete frontier, so it is not eligible for watermark early-stop. A later unbounded collection sync starts at the newest page, re-lists already cached records cheaply by revision, continues past the previous bounded frontier, and fills the missing tail before recording a new complete frontier.

## Partial Failure Handling

Bulk sync treats each listed issue or wiki page as an independent resource. A failure for one resource does not roll back resources that already synced successfully and does not prevent later resources from being attempted. If an issue collection page remains truncated or malformed after bounded adapter retries, complete schema-valid array elements before the decode failure are committed as successful resources. The malformed item and remaining bytes are discarded, pagination stops, and the run records a non-complete frontier so the page is retried rather than treated as covered.

When any resource fails, the service returns `PartialSyncError` with:

- `success_count` for resources committed successfully;
- `failure_count` for resources that failed;
- per-resource details including source id or remote alias, remote type, diagnostic message, `failure_class`, endpoint, HTTP status when known, and remediation hint when available.

Successful parent resources remain committed to the cache. Child-resource failures are grouped by remote type, diagnostic class, endpoint pattern, and status code in compact summaries so an operator can distinguish repeated route compatibility failures from isolated record failures. Failed resources are reported to the caller and can be retried with the same repository, selector, and idempotency key strategy.

Issue comment reads are secondary to primary issue collection sync. Parent traversal commits the issue as `fresh` and records comment coverage independently as `pending`, `deferred`, or `complete` in `issue_comment_sync`. An aggregate `--issue-comments` traversal that receives `rate_limited` stops without per-issue fan-out and leaves the queue retryable. The compatibility path marks the current per-issue item `deferred` before stopping. Later drains or targeted `--input issue:N` refreshes can retry comment coverage without changing or blocking the parent issue frontier. Reconciliation failures use `issue_comment_reconciliation` and keep affected coverage pending. `sync-status` reports both the primary source state and this secondary queue state. This keeps large repository backfills useful for search and RAG even when child comment endpoints are temporarily too expensive.

Actionable failure classes include:

- authentication or authorization failures from the live provider;
- rate-limit responses;
- network, timeout, and context-cancellation failures;
- partial or oversized provider responses;
- missing remote resources;
- cache integrity, write, or lock failures.

The CLI renders the aggregate success and failure counts and resource details. Diagnostics must stay public-safe: tokens, Authorization headers, cookies, private repository coordinates, and raw API bodies are not printed.

## RAG Cache State

RAG state is additive to the GitCode cache. The canonical source records and index chunks remain readable when no embedding provider is configured. Embeddings live in `chunk_embeddings`, keyed by `(repo_id, namespace_id, chunk_id)`, so deleting or rebuilding RAG coverage does not invalidate records, comments, sync events, snapshots, or chunk text.

An `embedding_namespaces` row captures provider/model identity: provider id and type, model id and revision, dimension, dtype, normalization, document/query instruction ids, chunk policy id, language policy id, and config hash. A provider, model, dimension, instruction, chunking, or language-policy change therefore creates a different namespace. The old namespace can be retained or removed without touching the GitCode cache.

The older `chunks.embedding` column is a legacy placeholder. It is nullable and not namespace-aware, so new RAG indexing must not treat it as canonical coverage. It remains in place for compatibility with existing schemas and read paths.

`rag_index_runs` stores long-running indexer progress: namespace, profile, status, total/embedded/skipped/failed counts, timestamps, error class, message, and small metadata. CLI/MCP progress surfaces can read this without contacting the embedding provider.

## Cache Migration

The implemented cache schema version is `21`, matching `currentSchemaVersion` in `internal/cache/schema.go`.

The primary version source is the SQLite `schema_version` table. Migrations also update `PRAGMA user_version` as an additive SQLite diagnostic bridge, but cache compatibility decisions use `schema_version`.

`repo status` and the MCP `repo_status` tool report the effective binary
identity together with detected/expected cache schema versions. They also
include cached issue/comment counts and the durable issue-comment queue
summary, so operators can distinguish binary skew, schema skew, incomplete
comment coverage, and an empty cache before attempting repair.

When the cache is on an older compatible schema, CLI `repo status` reopens it
read-only for diagnostics instead of failing before it can report the mismatch.
Fields introduced by a newer schema, such as the issue-comment queue in schema
16, are reported with `issue_comment_queue_state: "schema_unavailable"` until
the cache is migrated.

Compatibility policy:

| Detected version | Behavior | Operator action |
| --- | --- | --- |
| New empty cache | Initialize normally at schema version 21 | None |
| 21 | Open normally; reads and writes are allowed | None |
| 2-20 | Open read-compatible but writes are blocked until migration | Run `gitcode-mcp migrate-cache --confirm` |
| 1 | Block migration as pre-supported/iteration-1-equivalent | Confirm the selected cache path, move aside or delete only that cache file, then re-sync |
| 0, missing, or empty `schema_version` in a non-empty cache | Block as pre-schema-versioning or unknown | Confirm the selected cache path, move aside or delete only that cache file, then re-sync |
| Greater than 21 | Block as newer than this binary supports | Upgrade `gitcode-mcp` to a binary that supports the schema |

`gitcode-mcp migrate-cache --confirm` runs supported older-version migrations in place from the selected effective cache path, including repo-local cache selection when run from a repo-local workspace. Explicit `--cache-path` still overrides repo-local discovery for emergency repair. Before any schema mutation, the command inspects the coordinator identity and supported schema range. An installed coordinator is unloaded and observed until both its process and control socket are gone; an unowned foreground coordinator makes migration fail closed. The WAL is checkpointed, a backup is created at `{cache-path}.backup-{timestamp}`, and that backup must pass `integrity_check`, schema-version, and cache-identity verification. All pending schema steps then commit as one transaction, so a failed step leaves the original schema and identity intact. A private cache-adjacent recovery intent is written before coordination and retained until compatible service installation, restart, and schema-range health verification complete. While that intent exists, an invocation without `--confirm` returns `recovery_required` and cannot report `up_to_date`; re-running the confirmed command resumes the intent even when the schema transaction already committed. After verified restart, the CLI atomically publishes a completion receipt containing the private cache UUID, target binary identity/range, and backup/identity verification results, then clears the pending intent. Admin never exposes the UUID or filesystem locations: it accepts the receipt as success evidence only when both verification flags are true and its UUID, schema, binary identity, and range exactly match the live cache and daemon. Machine-readable output records quiesce, backup verification, identity preservation, compatible target identity, restart, and recovery state.

The daemon health and status contracts publish `binary_version`, sanitized `binary_commit`, and the exact operational `schema_min..schema_max` range. A mismatch is `cache_schema_blocked`, not generic `cache_unreadable`: maintenance suppresses new writers, recomputes `active_jobs` from genuinely active jobs, and publishes path-free detected/expected schema, running-daemon identity, range, and quiesce state through service status/health, jobs, maintenance, CLI, MCP, and Admin DTOs. The Admin lifecycle distinguishes migration required, unsafe downgrade refusal, interrupted recovery, and a compatible restart backed by the durable completion receipt; it exposes backup/migration/restart, data, and identity states without filesystem locations. Machine-changing work remains an explicit dialog that only copies the fixed `gitcode-mcp migrate-cache --confirm` handoff; browser controls never accept cache paths or perform schema mutation. Browser CI asserts text, DOM/ARIA, JSON, and command invariants only, with screenshots, traces, video, and visual baselines disabled.

Schema version 13 adds `pr_review_discussions` and `pr_review_positions`. The migration creates empty tables and does not invent position metadata for comments already cached under older schemas. A later pull request comment sync, `add-pr-review-comment`, or `reply-pr-review-comment` write refreshes the affected comment rows and position tables. PR comment `content_hash` includes position metadata, so a resync can update stale rows when the adapter merges richer v4 discussion data with v5 parent/reply relationships.

Schema version 14 adds `sync_frontiers` for issue and pull request collection traversal metadata. Existing caches start with no complete frontier rows after migration, so the first post-migration bounded sync will not early-stop from legacy record timestamps. A run that reaches `end_of_collection` or safely stops via a previous complete frontier records `status=complete`; bounded, timed-out, cancelled, and partial runs record non-complete statuses that are not eligible for early-stop.

Schema version 15 adds RAG namespace, chunk embedding, and index run tables. Existing records, chunks, snapshots, and sync frontiers are not rewritten. Existing `chunks.embedding` data, if present, is left untouched and treated as non-canonical legacy data.

Schema version 16 adds `issue_comment_sync`, a per-issue durable work queue keyed by repository and stable source id. Existing issue records are seeded lazily when an operator first runs `--issue-comments`; new parent backfills populate the queue as each issue is committed.

Schema version 17 adds a durable random cache UUID, independent maintenance head/tail frontiers, per-repository content generation, and per-namespace RAG covered generation. Existing sources and chunks remain valid; new hash-changing writes advance generation and cause daemon reconciliation to repair lagging RAG namespaces. See [Daemon Cache and RAG Maintenance](cache-maintenance.md).

Schema version 18 adds metadata-only repository-document revision sets, chunk identities, revision membership, and namespace-scoped vectors. It records Git object ids, content digests, byte and line ranges, policies, coverage, and lifecycle state; document bytes remain authoritative in Git and are read on demand.

Schema version 19 binds each revision set to an opaque source registration and generation, records the processing-policy identity, preserves worktree authority on membership rows, and adds typed exclusion metadata. Existing version-18 rows receive neutral defaults and remain inspectable; a fresh plan/index establishes the current registration identity before promotion to a ready set.

Schema version 20 adds checksum-bound durable sync commit receipts. A receipt is
written in the same transaction as cache graphs and frontiers so restart can
distinguish “SQLite committed, journal terminal write failed” from an uncommitted
stage without repeating provider traffic.

Schema version 21 adds `last_success_at` to maintenance frontiers. A degraded
observation can replace the current status and error while retaining the most
recent durable successful-sync timestamp for retry diagnostics and Admin UI.

Opening an older compatible cache without migration is read-compatible but write-blocked so operators can inspect the cache and run diagnostics before applying the migration. New caches are initialized directly at the current schema version.
