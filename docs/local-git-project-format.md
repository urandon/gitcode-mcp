# Local Git project format (schema 1)

This is the isolated implementation of epic #200. The G4 CLI/MCP checkpoint,
G5 search/index, G6 explicit protected Admin and G7 private Git bundle/restore
are implemented on this branch. Whole-feature rollout still requires final
whole-epic review and integration evidence. Its child PRs integrate into
`codex/epic-200-git-backed-harness`; the whole cohort enters `main` together.
Git owns all project content and history. No cache database is required by the
adapter, and no source or chunk bytes are copied into an index by this package.

## Git tree

```text
.harness/project.yaml
.harness/receipts/<64-lowercase-hex-key-hash>.json
issues/ISSUE-1/issue.md
issues/ISSUE-1/comments/COMMENT-1.md
wiki/DOC-1.md
```

The machine-local store is a dedicated service-owned bare repository, or the
ordinary `.git` directory of a dedicated non-linked repository. Tree paths are
not filesystem paths exposed to clients. Linked worktrees, object alternates
and redirected object directories are unsupported for this store; existing
repository-document readers keep their own unchanged worktree behavior.
Direct checkout editing, concurrent external ref mutation and history rewriting
are not silently supported workflows.

```yaml
schema: 1
project_id: example-project
next_issue: 2
next_comment: 2
next_document: 2
```

Project identity is a stable 1–64-character ASCII identifier. Counters are the
next unused display ID, greater than every corresponding ID still in the tree,
including tombstones. Positive canonical decimal IDs are bounded to the largest
exact JSON integer (9007199254740991). Comments use a project-wide counter, even
though their files live beneath an issue. A duplicated comment ID is invalid.

Entity files are UTF-8 Markdown with LF-delimited YAML frontmatter:

```markdown
---
schema: 1
id: ISSUE-1
kind: issue
title: Research question
state: open
labels: [research]
milestone: Phase one
links: [{kind: wiki, id: DOC-1}]
---
What should we measure?
```

Kinds are `issue`, `comment`, `wiki`; IDs are respectively `ISSUE-n`,
`COMMENT-n`, `DOC-n`. Issues have `open|closed|deleted` state; comments/wiki have
`active|deleted`. Comment frontmatter requires `parent_id: ISSUE-n`, matching its
tree path and an existing parent. Other kinds must not carry `parent_id`.
Only issues carry labels/milestone. Links contain a typed kind and stable ID in
this project; unresolved links are not silently converted to remote URLs.
Deletion retains a tombstone and Git history, not an erasure guarantee.

Unknown/duplicate fields, aliases, anchors, YAML merges, unsupported schemas, invalid IDs,
identity/path mismatches, binary/LFS/symlink/submodule entity files and malformed
metadata are rejected. Titles are at most 512 UTF-8 bytes, labels at most 32
unique values of 128 bytes, milestone at most 128 bytes, links at most 64.

Receipts contain only `schema`, `project_id`, `key_hash`, `intent_hash`,
`operation`, typed `result`, and `result_blob`. Hashes are 64 lowercase hex
characters; the result blob is a full Git object ID. Operation must match the
result kind. JSON key names are exact and case-sensitive, including nested
result fields. Unknown/duplicate keys and content fields are rejected. A receipt
does not contain its own producing commit ID: Git history supplies that locator.
At that commit, a delete result must be a tombstone with an empty body; other
operations must return live entities. Create/add results must be absent in the
immediate parent and consume exactly its next ID/counter, not claim an old ID.
That parent's state must permit the operation: edits/deletes target live entities
and a new comment has a live parent issue. Later deletion does not invalidate an
earlier valid receipt.
The mutation adapter publishes receipts with their entity and counters in one
commit; the reader can inspect the same files without opening a database.

## Read contract and limits

`Open` binds a canonical local Git directory. `Snapshot` resolves HEAD, a local
branch ref or full commit ID once; all get/list/citations then use that commit,
even if HEAD moves. Citations include project, kind, stable ID, exact commit,
blob and Git-tree path. `.harness` files are never entity-list/search content.
Entity validation is lazy at get/list; snapshot creation validates project,
fixed entity paths, file modes, ID uniqueness and counter consistency.

Ceilings may be tightened, not increased: file 1 MiB, metadata/receipt 16 KiB,
tree output 4 MiB, tree entries 10,000, one list at most 128 entities and 8 MiB
source bytes, history at most 256 first-parent commits. Public read operations
have a ten-second ceiling in addition to caller cancellation. History returns
the commit introducing each distinct entity blob on the service's linear
history. Historical project identity, file mode and entity frontmatter must
validate before a citation is returned. History also has an 8 MiB cumulative
source-byte budget; already validated blobs are reused within the one request.
A prefix too short to prove complete history returns
`history_unavailable`, not a complete or absent result. Limits return typed
errors with fixed fields, not private input, Git stderr or filesystem paths.

LocalGit uses bounded Git plumbing with an allowlisted environment, explicit
Git/object/common directory, no pager/replacement refs, no global/system config,
no optional locks, lazy fetch or allowed transport protocol, and disabled hooks,
credential helpers and signing. Blob reads do not apply filters/textconv.
The executable is resolved at local setup; this is process isolation, not an
OS-level network sandbox or a guarantee against a malicious Git installation or
concurrent out-of-band filesystem changes. See Git's
[environment contract](https://git-scm.com/docs/git#_environment_variables).

## Typed local mutation contract

The internal `Mutate` entry point accepts only `create_issue`, `update_issue`,
`delete_issue`, `add_comment`, `update_comment`, `delete_comment`, `create_wiki`,
`update_wiki`, and `delete_wiki`. Each generates the fixed tree paths above;
callers cannot submit arbitrary Git commands or file patches. It requires
`write_mode: local` and a non-empty caller idempotency key of at most 256 bytes.
This is a local Git write, not a remote GitCode write or an implicit read effect.

Creates require the expected full HEAD commit ID. Updates/deletes require an
expected entity blob revision or full HEAD, or both. Updates replace all editable
fields; identity and comment parent never change. Creates default to `open`
(issue) or `active` (comment/wiki); updates specify state. Only the explicit
delete operation creates a `deleted` tombstone with an empty current body.
It retains metadata, IDs, receipts and all previous Git history. Adding a comment
to a deleted issue and editing a tombstoned entity conflict.

One non-blocking OS writer lock covers the canonical Git directory, including
symlink aliases and separately opened processes. Its fixed lock file lives
outside the project tree; it is not a durable operation journal. A busy writer
returns `writer_busy`. The adapter builds blobs/changed trees, one parented
commit containing all authoritative effects, then publishes with
`update-ref --no-deref <configured-ref> <new> <expected-old>`. The configured
local branch must not be symbolic. No working-tree/index or SQLite write is
needed. Rejected candidates may leave unreferenced Git objects for ordinary Git
garbage collection; they are never treated as published operations.

The intent digest covers project identity, configured ref, operation, target
and content. Concurrency guards are excluded, so refreshing a stale guard does
not change a logical retry. A known receipt is resolved before the stale guard:
same key/intent returns the original entity, blob and producing commit with
`replayed: true`; changed intent returns `idempotency_conflict`. The original
result is read at the receipt's introducing commit, not from a later edited
entity. Receipts never carry copies of content or their self-referential commit
hash and are never silently changed or pruned.

The complete bounded HEAD tree is authoritative for receipt-key presence.
Resolving a present receipt's origin is a bounded first-parent lookup; exhaustion
returns `history_unavailable`, never key absence and never a duplicate mutation.
New distinct keys do not require proving the entire project history. Removing
receipts, external ref mutation or rewriting history is outside the supported
service-owned workflow. Capacity exhaustion (including the 10,000-entry limit)
requires an explicit future archival policy, not implicit receipt pruning.

Every write has the same ten-second ceiling/caller cancellation and input,
metadata, receipt and tree limits as reads. Candidate validation happens before
publication. Cancellation before the ref update leaves the ref unchanged. After
publication, receipt readback confirms the result. Definite concurrent ref
mismatch returns `write_conflict`; an observed unchanged ref after a rejected
update returns `publication_rejected`. Lost response or unavailable readback
returns `publication_ambiguous`: recover using the same key, never blind retry
with another key. This does not certify power-loss durability beyond Git's own
object/ref publication semantics.

## Strict-local service hosting (internal epic cohort)

Global machine configuration selects the existing daemon's local startup branch:

```yaml
service:
  mode: local
local_projects:
  lab:
    backend: local_git
    project_id: example-project
    storage_ref: lab-data
    ref: refs/heads/main
    read_only: false
    rag_profile: disabled
```

At most 32 registrations use bounded ASCII selectors/storage references. An
opaque `storage_ref` resolves to `<user-config-dir>/gitcode-mcp/local-projects/
<storage_ref>.git`; it is not a path or URL input. These are dedicated Git data
repositories, not repository-document source checkouts. Setup and registration
are machine-local CLI handoffs, implemented by the separate vertical-slice child
#204 below. G3 initializes the registered service from an already valid Git store;
it never automatically adopts an experimental store. `local_projects` requires
explicit `service.mode: local`; a missing/mistyped mode cannot silently start a
remote host. Repository-owned config cannot set either field.

`Manager.Run` takes the local branch before creating remote jobs, maintenance,
cache authority, credential reporters or embedding providers. By default it
does not construct an Admin controller either.
It reuses the existing runtime state, private socket, handler cancellation and
shutdown joining. The control socket is mode 0600 in its private runtime area.
TCP, redirected socket addresses, live remote intent and implicit Admin are
rejected. An already-live daemon is not replaced/cohosted. Explicit protected
loopback Admin is separately explicit through `service run --admin` (G6 below),
and is not a zero-network claim. `admin open` cannot turn a strict-private local
host into an HTTP host; restart with the explicit opt-in first.
There is no new daemon/coordinator, ownership ledger or OS-level sandbox.

Existing private JSON-RPC exposes the following shared-service calls, reused by
the explicit G4 CLI/MCP tools below:

| Method | Request/result |
|---|---|
| `LocalProjects.Status` | Empty request; selector/identity, readable/read-only state, exact commit and finite last-conflict observation; no content, storage paths or provider URL |
| `LocalProjects.Get` | `project`, `kind`, `id`; one exact Git entity/citation |
| `LocalProjects.List` | `project`, `kind`, bounded `offset`/`limit`; exact Git entities |
| `LocalProjects.History` | `project`, `kind`, `id`, bounded `max_commits`; Git citations |
| `LocalProjects.Mutate` | `project`, typed `mutation` from the G2 contract; entity and replay flag |

Canonical store aliases share one runtime/OS writer lane. Conflicting project
identity or configured-ref aliases are rejected. Read-only aliases reject writes.
Project identity is checked on reads and inside the mutation writer lane before
receipt lookup/publication; a registration never falls back to a GitCode repo.
Last-conflict observation is process-local and resets after successful writes
or daemon restart; it is not a lock, receipt, journal or promise of future write
availability. Only Git is durable authority.

Local calls own one private connection, inherit a ten-second ceiling and cancel
the in-flight Git command when the caller disconnects. JSON keys/IDs/envelopes
are strict, requests retain the existing 1 MiB wire cap, and escaped-JSON
responses are capped at 64 MiB over the reader's 8 MiB source budget. Only
registered selectors and typed fields are accepted; unknown routes/methods,
malformed input and transport failures return finite diagnostics without raw
paths/provider bodies. The local host's lifecycle health/status remains readable;
remote job/maintenance/doctor RPC actions are unavailable, not fallback. Admin
open/status are unavailable in strict-private mode; the explicit protected G6
host reuses those existing lifecycle calls.
After a lost reply/restart, retrying the same key returns the Git receipt's
original result even if the entity was subsequently edited.

Reader, writer and private-service tests create real temporary Git stores
without credentials or external network. G5 adds explicit search/index below;
G6 adds protected Admin below; G7 adds private bundle/restore below.

## CLI/MCP first usable checkpoint (G4)

Use a trusted global YAML selected through `GITCODE_MCP_CONFIG`, or the normal
user configuration. Setup preserves unrelated YAML fields and atomically writes
`service.mode: local` plus one registration. It does not start, stop or replace
a running daemon. Stop a remote/cache daemon explicitly before using the same
runtime, or configure a distinct machine-local `service.runtime_dir`. Strict
local daemon hosting never cohosts remote maintenance or implicit Admin HTTP.
JSON configuration is readable but setup requires a trusted YAML handoff.

```sh
gitcode-mcp project init --project lab --project-id example-project \
  --storage-ref lab-data --confirm
gitcode-mcp service run
```

Keep the daemon running; use a second terminal with the same configuration:

```sh
gitcode-mcp project status
gitcode-mcp project mutate --project lab --operation create_issue \
  --title 'Research question' --body 'What should we measure?' \
  --expected-head EXPECTED_FULL_COMMIT --write-mode local \
  --idempotency-key research-question-1
gitcode-mcp project get --project lab --kind issue --id ISSUE-1
gitcode-mcp project list --project lab --kind wiki --limit 20
gitcode-mcp project history --project lab --kind issue --id ISSUE-1 --max-commits 64
gitcode-mcp --mcp
```

Copy `EXPECTED_FULL_COMMIT` from `project status`, never guess it. Creates
require the full expected head; update/delete accept `--expected-revision` from
the entity's exact citation or an expected head. The operation enum is the G2
contract above. CLI metadata flags are `--state`, `--labels` (comma-separated),
`--milestone`, `--parent-id` for a new comment and `--links` (a JSON array of
typed `kind`/`id` references). `--body-file` is CLI-only; `-` reads bounded stdin.
Updates replace editable fields, including empty body/labels/links; explicitly
preserve data you intend to retain. Only delete operations create tombstones.
Use `--links '[]'` to explicitly clear links. The array accepts at most 64
closed `kind`/`id` objects; null, duplicate, unknown and aliased fields are
rejected. Explicit `project` commands load trusted global/environment settings
only. Invalid or missing selected configuration returns
`local_git_invalid_configuration` without credential discovery or cwd fallback.

`project init` refuses any existing destination rather than overwriting or
implicitly importing it. If initialization succeeded but config registration
failed, inspect the cause and use explicit registration of that schema-1 store:

```sh
gitcode-mcp project register --project lab --project-id example-project \
  --storage-ref lab-data --confirm
```

Registration validates identity/ref and canonical aliases across the entire
candidate config under the existing trusted-config writer lock. Existing
selectors cannot be silently retargeted. Setup takes no arbitrary destination
URL; storage references resolve only in the managed local store directory.
There is no second operation journal. Successfully initialized Git data is not
deleted when a later registration fails. Initialization also preserves the store
once ref publication is attempted, even if cancellation leaves the result
unacknowledged: inspect it and explicitly register a valid schema-1 store rather
than retrying `init` over it. Config registration rejects rendered YAML above its
1 MiB intake limit before changing the selected file.

Local MCP discovery is side-effect-free and advertises only:

| MCP tool | CLI equivalent | Input |
|---|---|---|
| `local_project_status` | `project status` | Empty object; bounded registrations/status, no content or paths |
| `local_project_get` | `project get` | `project`, `kind`, stable `id` |
| `local_project_list` | `project list` | `project`, `kind`, optional `offset`/`limit` (default 20, max 128) |
| `local_project_history` | `project history` | `project`, `kind`, `id`, optional `max_commits` (default 64, max 256) |
| `local_project_mutate` | `project mutate` | `project` and closed typed `mutation` with local intent, expected revision/head and caller key |

For example, pass this to `local_project_mutate`:

```json
{
  "project": "lab",
  "mutation": {
    "operation": "update_issue",
    "id": "ISSUE-1",
    "title": "Answered research question",
    "body": "Retained evidence and result.",
    "state": "closed",
    "expected_revision": "EXPECTED_FULL_BLOB",
    "write_mode": "local",
    "idempotency_key": "answer-question-1"
  }
}
```

Local write tools are hidden and rejected before service access when MCP access
is `read`. `write` discovery does not bypass local intent/revision/key checks.
Storage setup is CLI-only; MCP never accepts paths, store refs or provider URLs.
Stdio uses the ordinary MCP transport. Explicit HTTP/SSE uses the same local
handler but must bind a loopback IP; that opt-in listener is not a zero-network
deployment claim. Startup and the G4 operations resolve no credentials, construct
no GitCode/embedding providers, initialize no cache and start no daemon. G5's
explicit optional embedding operations below remain separate from that default.

CLI defaults to structured JSON. Results carry entity metadata/body and exact
commit/blob/Git-tree citations; writes also carry `replayed`. Missing cache data
does not affect these operations. Replaying the same key/intent after later
edits or daemon restart returns the original result. A different intent conflicts.
After a timeout or `local_git_publication_ambiguous`, retain the same key; never
blindly create a second request with another key. Stale revisions return
`local_git_write_conflict`. Exhausted history returns
`local_git_history_unavailable`, not a complete or absent history. Unknown
project/configuration/service failures remain typed and never select GitCode.
Current reads and returned historical citations remain Git-backed; direct
checkout editing/import, history rewriting and standalone label/milestone CRUD
are outside this checkpoint.

## Local project search and derived indexing (G5)

Both transports reuse the existing repository-document retriever, chunker,
immutable revision sets, vectors and digest-verified hydration. The adapter
passes one validated LocalGit snapshot in bounded request-local memory. Only
active `issues/**` (including comments) and `wiki/**` entity files are eligible;
tombstones, `.harness/**`, counters and receipts are excluded. Fixed internal
corpus policy cannot be expanded by a client or by a source checkout config.
Every hit has its project/kind/stable-ID/commit/blob/tree-path citation, bounded
snippet, line range, raw-slice digest and score. The 8 MiB source/tree/file and
ten-second request limits still apply; oversized corpora fail explicitly.

```sh
gitcode-mcp project search --project lab --query 'research evidence'
gitcode-mcp project search --project lab --query 'research evidence' --mode hybrid
gitcode-mcp project index --project lab --write-mode local --max-chunks 64
```

MCP exposes `local_project_search` (`project`, `query`, optional `mode`, `limit`)
and `local_project_index` (`project`, `write_mode: local`, optional `max_chunks`).
Search defaults to `fulltext`, maximum query 4096 UTF-8 bytes, default 10 hits,
maximum 32. It requires neither cache nor provider. A hybrid query must be
explicit and the registration must name an already provisioned embedding
profile. Queries never install/download/start/probe setup, repair, initialize
an index or contact GitCode. The query alone is embedded; document transfer
happens only in the explicit index action.

Strict local mode ignores repository-owned configuration from the process's
working directory. Only trusted global settings/environment configure providers
and cache selection; an unrelated checkout cannot retarget local project bytes.
Restart the local daemon explicitly after changing machine configuration.

CLI setup accepts `--rag-profile EXISTING_PROFILE` (default `disabled`) when
creating/registering a new selector. An existing registration is never silently
retargeted; change its trusted global YAML deliberately when selecting a profile.
Only configured `local_process`/`local_network` boundaries are allowed; remote,
unknown or absent boundaries fail closed before embedding. Provider requests
have bounded deadlines and 4 MiB response bodies, do not follow redirects or
environment proxy settings, and never invoke setup/model-management commands.
Every query/batch/item embedding must match the selected model revision and
dimensions before scoring or persistence; model drift cannot silently enter an
old namespace. Missing or malformed derived vectors cause lexical fallback,
whose coverage describes the effective lexical result. Citation line ranges
are recomputed from pinned Git bytes, not trusted from index metadata.

Index is an explicit bounded foreground call through the same private IPC, not
a new job protocol. It embeds at most `max_chunks` new chunks (default 64,
maximum 128) and returns exact commit, revision-set ID, ready/partial state and
embedded/reused/failed counts. Repeat a partial/interrupted call to reuse the
existing engine's immutable metadata and vector-only checkpoints; no new project
receipt or authority journal is created. Read-only registrations and read-only
MCP policy reject index writes. No index action changes project Git content.
The existing `embedded_chunks` counter also includes replayed provider handoffs;
it can exceed 128 on resume without new provider work exceeding `max_chunks`.
All returned counts remain bounded by eligible corpus chunks and conserved.

The existing SQLite schema/engine is used in a separate disposable file named
by appending `.local-project-index.db` to the selected filesystem cache path
(SQLite URI and in-memory selections require a file-path handoff). This keeps
internal metadata scope keys out of remote GitCode repository bindings; these
keys have no owner, remote API endpoint or sync scopes. Only metadata, locators,
digests and vectors are persisted, never Markdown or chunk text. The normal
selected cache is not initialized by local project operations. Existing derived
retention keeps eight committed sets per identity for 30 days with a 512 MiB
vector ceiling; vector-only handoffs are bounded to seven days/512 MiB.

Search reports `requested_mode`, `effective_mode`, `index_state`, `fallback`,
coverage and hits. Missing/stale/partial/failed/unusable semantic state visibly
falls back to fresh Git lexical results. Exact commit/policy/model namespace and
content digests prevent an edit or tombstone from reusing a stale revision set;
unchanged chunks can reuse vectors during explicit reindexing. Deleting derived
state loses no project content: fulltext still works, hybrid reports fallback,
and explicit index rebuilds it. Cache/schema failures and invalid derived
citations cannot hide valid current lexical matches. Historical Git citations
remain independent of derived-cache retention.

## Protected Local projects Admin (G6)

Strict-private `service run` starts no Admin HTTP listener, not even after an
`admin open` request. To opt into the existing protected loopback console using
the trusted local configuration:

```sh
gitcode-mcp service run --admin
# In another terminal, with the same trusted configuration/runtime:
gitcode-mcp admin open
```

Only loopback IP binds are accepted in local mode. A bind without `--admin`,
non-loopback address, or unsafe override is rejected. This does not change the
remote/cache host's existing lazy-Admin behavior. There is no second daemon.
Default local startup still does not discover credentials, load remote caches,
register maintenance or provision an embedding provider. The opted-in local
snapshot contains only lifecycle state and the local capability catalog.

**Local projects** uses the same shared Git service for configured selector
availability, read-only state, bounded issue/comment/wiki list/read, exact
commit/blob/tree citations, history, editing, tombstones and search. Search
shows requested/effective mode, missing/stale/partial/failed index state and
lexical fallback. Ordinary reads never create an index. Source content is
escaped text, not executed HTML/Markdown. Drafts and retry intents stay in
browser memory only, never URLs or browser storage. The shell retains one
bounded ephemeral editor instance across sections; it performs no cold local
reads until selected. Ordinary late reads are canceled when leaving the cohort,
while an explicitly confirmed write retains its exact retry intent. Full-page
departure with a draft or uncertain write requests the browser's discard
confirmation instead of silently losing that in-memory state.

The authenticated `/api/admin/v1/local-projects` GET returns status. Fixed POST
suffixes `list`, `get`, `history`, `search`, `mutation-plan`, `mutation-apply`,
`index-plan`, `index-apply` accept closed typed JSON, not URL selectors. Even
content reads use session/Origin/Fetch Metadata/CSRF protection. Every operation
inherits a ten-second context, 1 MiB request cap and 64 MiB escaped-response cap.
The normal/cache host advertises this cohort as unavailable.

Mutation plans are stateless content-free digests of the exact intent and
trusted registration; they are not another journal. Preview uses the writer's
validation and guards without creating objects, receipts, counters or commits.
Apply checks the confirmed digest, then delegates to the existing writer, where
known Git receipts resolve **before stale guards**. Creation requires full
expected HEAD; edit/tombstone requires the inspected blob. Changing editor
inputs invalidates a plan. Conflict retains the draft and original guard;
explicit current-entity inspection and revision adoption can prepare a new
review without overwriting the draft. Uncertain delivery locks editing and
retains the exact confirmed intent/key for an explicit retry. No automatic
write retry occurs. A replay receipt names the original result, not current
HEAD; current state is refreshed separately.

Index plans pin commit, configured profile/provider and bounded new-chunk cap.
Confirmation calls the existing foreground indexer, with metadata-only result
receipts in the existing Admin control receipt store. These receipts never
contain source bodies, chunks or filesystem paths and are disposable; Git is
the sole project authority. The chunk cap is per foreground attempt; interrupted
derived work can resume/reuse existing vectors, not claim exactly-once provider
execution. Completed index requests replay their original metadata receipt.
Provider setup/download/start, storage selection, import/restore and history
rewriting remain CLI handoffs. No browser-entered destination/path is accepted.

Deterministic tests exercise a real Git store through Unix IPC plus authenticated
HTTP, independent of credentials or external network, and semantic browser
tests cover confirmation/cancel, lost replies, conflict/draft recovery,
read-only/unavailable projects, lexical fallback, themes and narrow layout.
Complete feature rollout remains gated by whole-epic review/integration.

## Private Git backup and restore (G7)

Git contains every authoritative project byte, ID, counter, receipt and revision.
The backup is an ordinary self-contained [Git bundle](https://git-scm.com/docs/git-bundle),
not a semantic snapshot format or a copy of a SQLite authority. It contains the
configured branch and its complete reachable history, including tombstoned
content. **Keep it private.** Do not attach real bundles to issues/PRs, publish
them in this code repository, or treat tombstones as an erasure guarantee.
Encryption, off-machine storage and retention are deployment/operator concerns.

Export uses trusted machine configuration and the existing store writer lane;
the daemon may be running, but concurrent writes can return `writer_busy`.
Confirm the file effect explicitly:

```sh
gitcode-mcp project export --project lab --bundle lab.bundle --confirm
```

No existing output file is overwritten. The new file is mode 0600. Structured
output names only selector, stable project ID, exact commit, bounded commit
count, status and next action; it never embeds the file or a filesystem path.
Keep the full `commit` alongside the bundle for restore verification. Export
validates source history, uses an explicit revision/path separator, detects an
out-of-band HEAD change, and bounds binary output in request-local memory.
Binary bundle bytes never enter normal tool responses or logs.

Restore requires a new absent managed store, an unregistered selector and an
inactive daemon in the selected configuration. Use a trusted global YAML on the
destination machine (possibly initially absent), not repository-owned config:

```sh
# Explicitly stop any daemon using this selected runtime before restoring.
gitcode-mcp service stop
gitcode-mcp project restore --project lab --project-id example-project \
  --storage-ref restored-data --ref refs/heads/main \
  --bundle lab.bundle --expected-head EXPECTED_FULL_BUNDLE_COMMIT --confirm
gitcode-mcp service run
# In a second terminal using the same configuration:
gitcode-mcp project get --project lab --kind issue --id ISSUE-1
gitcode-mcp project history --project lab --kind issue --id ISSUE-1 --max-commits 64
```

`project-id` and ref must match the backed-up identity and branch, not invent a
new identity. `expected-head` is the full export commit, not a short SHA. Store
references use the same managed directory rule as setup. Existing destinations
(even empty directories), registered selectors, storage references and duplicate
project authorities are rejected, never retargeted. On the original machine,
deliberately remove an obsolete registration from trusted YAML only after
stopping that daemon, or restore in another trusted configuration/machine.
No automatic experimental-store import, SQLite reinterpretation, active-store
replacement, history rewriting, provider setup or daemon restart occurs.

The CLI observes selected daemon PID state before restore. It is an operational
guard over the existing lifecycle, not OS-level sandbox/descriptor attestation.
The filesystem no-overwrite rule and final complete-candidate registration lock
are still authoritative if setup calls race. Out-of-band filesystem changes,
malicious Git installations and concurrently launched unregistered processes
remain outside the service-owned-store contract.

Restore snapshots a bounded regular local file privately, rejects incremental
prerequisites, filtered bundles or additional refs, checks the exact advertised
ref/HEAD, imports objects into a new private bare store, runs strict Git object
validation and validates the complete linear project history before publishing
the branch/registering it. SHA-1 and SHA-256 object formats retain their exact
commit identities. It verifies schema/project identity on every commit, fixed
regular entity paths/metadata/parents, monotonic counters, retained entities and
unchanged receipts, including each receipt's result blob at its introducing
commit, operation outcome and fresh creation allocation in the immediate parent.
Removed/changed or internally inconsistent receipts cannot become an unsafe
same-key retry.

Bounds: 64 MiB input/export bundle, at most 40,256 advertised pack objects;
existing 1 MiB entity, 16 KiB metadata/envelope, 4 MiB tree/10,000 entries,
256 commits and 8 MiB distinct validated source blobs. Distinct validation
metadata is also bounded by 10,000 entries. Git commands and whole export/restore
have a ten-second ceiling/caller cancellation. Pack import uses a small delta
cache/large-blob streaming threshold. These are application/Git operation
limits, not a native allocator or power-loss certification. Exhaustion returns
typed `limit_exceeded`/`history_unavailable`/unavailable errors, never a truncated
successful backup. Larger histories need an explicit future archival workflow;
no receipt pruning or partial-history success is silently substituted.

Invalid unregistered imports are cleaned up by their creating operation. Once
validated ref publication is attempted, the store is preserved even after
ambiguity; inspect it instead of overwriting/retrying restore. A successfully
restored Git store also survives a later registration failure. Repair the
trusted configuration, then explicitly recover registration:

```sh
gitcode-mcp project register --project lab --project-id example-project \
  --storage-ref restored-data --ref refs/heads/main --confirm
```

The config uses the existing atomic locked registration writer, not a backup
journal. Current project reads and citations/history come from the same restored
commits/blobs. Same-key logical mutations return their original Git receipt
results before stale guards; new stale writes still conflict. Normal Git
process-crash/object/ref semantics apply, not a new sudden-power-loss guarantee.

Derived SQLite/vector files are not exported or restored. After losing them,
strict-private startup, get/list/history/mutate and full-text still work;
explicit hybrid reports lexical fallback until the existing index is rebuilt.
To opt in again, configure a pre-provisioned local-boundary profile in trusted
global YAML, restart the daemon explicitly and use bounded `project index`.
Index chunk caps are per attempt; interrupted derived work reuses the existing
engine, not a new project receipt protocol. No model install/download happens.

MCP discovery does not offer bundle uploads/restore/file selectors. The existing
protected Admin exposes restored project status, guarded edits, citations,
history and search, with backup/setup as CLI handoffs. Default `service run`
still has only private IPC; `service run --admin` explicitly enables protected
loopback HTTP and is not a zero-network claim. Existing GitCode/cache mode is
unchanged. No release tag or local installation is performed by this workflow.
