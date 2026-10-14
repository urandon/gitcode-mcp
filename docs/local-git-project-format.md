# Local Git project format (schema 1)

This is the isolated implementation of epic #200. The G4 CLI/MCP checkpoint
below is usable on this branch; search/RAG, Admin and whole-feature rollout
remain gated by the later children. Its child PRs integrate into
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
cache authority, credential reporters, embedding providers or Admin controllers.
It reuses the existing runtime state, private socket, handler cancellation and
shutdown joining. The control socket is mode 0600 in its private runtime area.
TCP, redirected socket addresses, live remote intent and implicit Admin are
rejected. An already-live daemon is not replaced/cohosted. Explicit protected
loopback Admin remains the separate #206 cohort and is not a zero-network claim.
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
remote job/maintenance/Admin/doctor RPC actions are unavailable, not fallback.
After a lost reply/restart, retrying the same key returns the Git receipt's
original result even if the entity was subsequently edited.

Reader, writer and private-service tests create real temporary Git stores
without credentials or external network. Search/RAG, protected Admin and bundle
restore remain separate required children of #200.

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
deleted when a later registration fails.

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
deployment claim. Neither transport resolves credentials, constructs GitCode or
embedding providers, initializes a cache or implicitly starts the daemon.

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
