# Repository documentation RAG

Repository documentation RAG searches documentation that is already versioned
in the local Git repository. Git is the sole durable source of document bytes.
The SQLite cache stores only revision-set metadata, chunk locators and digests,
membership, and embedding vectors. It does not copy README or `docs/` text into
another document store.

## Repository-owned policy

Corpus intent belongs in the tracked `.gitcode/gitcode-mcp.yaml` file so a
policy change is reviewed, branched, and released with the documentation it
selects. Machine-local provider endpoints, models, credentials, cache paths,
and resource limits remain outside this section.

With no `repository_docs` section, the versioned `conventional-docs-v1` preset
selects root `README*`, root `AGENTS.md`, and `docs/**`:

```yaml
cache_mode: repo-local
```

An explicit policy may extend or replace that preset:

```yaml
repository_docs:
  schema: 1
  enabled: true
  preset: conventional-docs-v1
  include:
    - architecture/**
    - runbooks/**/*.md
  exclude:
    - docs/generated/**
```

Use `preset: none` with at least one `include` entry for a completely explicit
corpus. Patterns are repository-relative, deterministic, and fail closed when
the schema or a pattern is invalid. Symlinks, untracked files, `.git/**`, and
generated `.gitcode/mcp/**` state are never eligible.

The implementation enforces an internal bounded-read safety limit while
streaming Git objects. That bound is not a second document-retention policy:
excluded large objects remain in Git, and no source bytes are persisted in the
cache.

## Revisions and overlays

Every query resolves exactly one commit and policy hash. A revision set is
identified by canonical repository id, opaque Git-store reference, commit,
policy, chunk policy, embedding namespace, and optionally a worktree-overlay
digest. Moving `HEAD` never changes an existing set.

The default authority is the committed tree. `--include-worktree` is explicit
and includes tracked changes only. Modified, deleted, and renamed files affect
the overlay digest; untracked files and symlinks are excluded. If the worktree
changes during indexing, the job publishes `superseded` rather than presenting
stale bytes as current.

Repository aliases are accepted at the command boundary, then resolved to the
canonical cache binding before any revision set, chunk, vector, or daemon job
identity is written.

## Commands

Run commands from the intended worktree; no command fetches Git objects or
contacts GitCode:

```sh
gitcode-mcp repo-docs policy --repo owner/repo
gitcode-mcp repo-docs plan --repo owner/repo --revision HEAD
gitcode-mcp repo-docs index --repo owner/repo --detach
gitcode-mcp repo-docs status --repo owner/repo
gitcode-mcp repo-docs search --repo owner/repo "writer lease"
gitcode-mcp repo-docs search --repo owner/repo --mode fulltext "writer lease"
gitcode-mcp repo-docs search --repo owner/repo --revision v0.2.0 "migration"
gitcode-mcp repo-docs search --repo owner/repo --include-worktree "draft runbook"
```

`index` always submits daemon-owned work and returns or attaches to its public
job id. When the selected cache/repository already has a maintenance
registration, this explicit local action also registers the worktree path in
the daemon's private `0600` registry. Public status exposes only opaque Git
store/worktree refs. The coordinator then polls committed `HEAD` and policy
identity every minute and coalesces a new immutable-set job when either
changes; it never fetches Git objects or contacts GitCode. Cache writers are
serialized with sync and ordinary RAG indexing.
Equivalent work coalesces. Interrupted sets are resumable, rename-identical
chunks reuse vectors, and deterministic metadata GC protects the current ready
committed set and matching ready overlay before removing unreferenced derived
state. Defaults retain at most eight committed sets for 30 days, retain an
explicit overlay for a 24-hour diagnostic grace period, and retain orphaned
terminal state for seven days. A machine-local vector-byte ceiling defaults to
512 MiB and can be changed with `GITCODE_MCP_REPO_DOC_VECTOR_BYTES`; this
resource limit is deliberately not part of the committed corpus policy. GC job
events report deleted sets/chunks/vectors and vector bytes before/after.

`fulltext` scans the exact Git blobs and needs neither an embedding index nor a
provider. `hybrid` fuses lexical and semantic ranking when the matching
revision set is ready. Missing or stale semantic state degrades transparently
to lexical retrieval. Responses distinguish `requested_mode` from
`effective_mode`, report committed Git versus explicit tracked-overlay
authority, and include the overlay digest when applicable. Every result is
hydrated from the exact blob or verified tracked overlay range and
digest-checked before its bounded snippet and citation are returned.

## MCP and Admin UI

MCP exposes `repository_docs_policy`, `repository_docs_status`,
`repository_docs_search`, and the asynchronous `repository_docs_index` job
submission tool. The caller's MCP working directory supplies local Git
authority; job progress and completion use the ordinary service job tools.

Admin UI repository details include a **Documentation** tab with policy,
revision-set identity, coverage, commit/overlay authority, namespace, and
daemon job observation. The browser is deliberately not allowed to select or
receive arbitrary absolute filesystem paths. The first explicit CLI index from
a worktree attaches that path to an existing daemon maintenance registration;
Admin then shows registration/reconciliation state and next poll time. Exact
index/search remain copyable CLI handoffs in v1, so browser requests never
carry a filesystem path.

Public surfaces expose only opaque `git_store_ref`, `worktree_ref`, `cache_ref`,
commit/blob ids, repository-relative paths, line ranges, and digests. Absolute
paths and source bodies are not part of status, job, diagnostic, or Admin
snapshot contracts.

## Failure and recovery model

- Invalid committed config: `repository_docs_policy_invalid`; correct and
  commit the policy.
- Missing local object: `git_object_unavailable`; fetch it explicitly outside
  the read path, then retry.
- Provider unavailable: full-text remains available; prepare the configured
  provider and submit a bounded index job for hybrid retrieval.
- Dirty overlay changed: `worktree_overlay_stale` or `superseded`; re-run with
  explicit `--include-worktree`.
- Writer contention: `cache_writer_busy`; inspect the active daemon job and
  retry after it reaches a terminal state.

No recovery path silently fetches, installs a model, copies document bodies,
or changes repository policy.

Release evidence and manual checks are defined in [Repository documentation RAG
release gates](repository-documentation-rag-release-gates.md).
