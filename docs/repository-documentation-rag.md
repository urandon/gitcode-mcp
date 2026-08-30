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

Register the private local Git authority once. The absolute path is retained
only in the daemon's mode-`0600` registry; subsequent commands use the three
opaque values returned by `register`. No command fetches Git objects or
contacts GitCode:

```sh
gitcode-mcp repo-docs register --repo owner/repo --repository-path /path/to/worktree

SOURCE_FLAGS="--registration-id REG --source-registration-id SOURCE --source-registration-generation 1"
gitcode-mcp repo-docs policy --repo owner/repo $SOURCE_FLAGS
gitcode-mcp repo-docs plan --repo owner/repo $SOURCE_FLAGS --revision HEAD
gitcode-mcp repo-docs index --repo owner/repo $SOURCE_FLAGS --detach
gitcode-mcp repo-docs status --repo owner/repo $SOURCE_FLAGS
gitcode-mcp repo-docs search --repo owner/repo $SOURCE_FLAGS "writer lease"
gitcode-mcp repo-docs search --repo owner/repo $SOURCE_FLAGS --mode fulltext "writer lease"
```

`index` always submits daemon-owned work and returns or attaches to its public
job id. It never infers private authority from the shell or MCP working
directory. A stale generation fails closed; replacing an authority is a
generation-checked compare-and-swap operation:

```sh
gitcode-mcp repo-docs rebind --repo owner/repo --registration-id REG \
  --source-registration-generation 1 --repository-path /new/worktree
```

Public status exposes only opaque Git
store/worktree refs. The coordinator then polls committed `HEAD` and policy
identity every minute and coalesces a new immutable-set job when either
changes; it never fetches Git objects or contacts GitCode. Cache writers use
the same cross-process lease as sync, migration, and projection writes, with a
bounded wait and durable replay of already-produced vectors.
Equivalent work coalesces. Interrupted sets are resumable, rename-identical
chunks reuse vectors, and deterministic metadata GC removes unreferenced
derived state under the configured retention limits. Defaults retain at most
eight committed sets for 30 days, retain an
explicit overlay for a 24-hour diagnostic grace period, and retain orphaned
terminal state for seven days. A machine-local vector-byte ceiling defaults to
512 MiB and can be changed with `GITCODE_MCP_REPO_DOC_VECTOR_BYTES`; this
resource limit is deliberately not part of the committed corpus policy. GC job
events report deleted sets/chunks/vectors and vector bytes before/after.
Vector-only provider checkpoints are independently bounded to seven days and
512 MiB, prune orphan identities when the durable admission set is known, and
discard corrupt derived checkpoints so the exact provider request can be
recomputed. Explicit job cancellation is written to the durable admission
registry and is not relaunched by reconciliation; repeated failures retain the
same public job identity while observing bounded exponential retry backoff.
The cancel operation acknowledges a repository-document job only after its
durable admission tombstone is committed; a state-write failure leaves the
worker running and returns a typed retryable error instead of false success.
An in-flight semantic query first loads one exact published revision-set
snapshot (membership, chunk locators, and vectors) transactionally. Retention
may evict historical cache rows after that snapshot is loaded without
invalidating the query; citations continue to hydrate from Git.

`fulltext` scans the exact Git blobs and needs neither an embedding index nor a
provider. `hybrid` fuses lexical and semantic ranking when the matching
revision set is ready. Missing or stale semantic state degrades transparently
to lexical retrieval. Responses distinguish `requested_mode` from
`effective_mode`, report committed Git versus explicit tracked-overlay
authority, and include the overlay digest when applicable. Every result is
hydrated from the exact blob or verified tracked overlay range and
digest-checked before its bounded snippet and citation are returned.
Responses preserve legacy `warnings` strings and also expose
`warning_details` with stable `code` and human-readable `message` fields for
automation and UI remediation.

Indexing repository content is allowed only through a provider whose effective
`data_boundary` is `local_process` or `local_network`. Profiles declared as
`remote`, `unknown`, or without an explicit boundary are rejected before a job
is created. Full-text search remains available without an embedding provider.

## MCP and Admin UI

MCP exposes `repository_docs_policy`, `repository_docs_plan`, `repository_docs_status`,
`repository_docs_search`, and the asynchronous `repository_docs_index` job
submission tool. Every call requires the same opaque registration, source, and
generation selector used by the CLI when more than one authority is registered;
the selector may be omitted only for an unambiguous sole authority. The daemon
alone resolves it to private Git authority. Generic service-job list/status,
attach, and cancel tools expose bounded public job lifecycle state.

Admin UI repository details include a **Documentation** tab with policy,
revision-set identity, coverage, commit/overlay authority, namespace, and
daemon job observation. It selects among opaque registered authorities, exposes
a generation-checked CLI rebind handoff, previews the exact eligible byte/file
plan and effective matchers, copies a commit-ready policy fragment, displays
typed warnings/exclusion counts, and reports derived-state retention/GC limits.
The browser is deliberately not allowed to select or
receive arbitrary absolute filesystem paths. The explicit CLI `register`
action attaches a worktree path to an existing daemon maintenance registration;
Admin then shows registration/reconciliation state and next poll time. Once
registered, the operator may confirm an immediate HEAD/policy reconciliation,
open the resulting daemon job for attach/cancel supervision, or search an
explicit Git revision. Those browser requests carry only the opaque
registration/source/generation selector, revision, query, mode, limit, and tracked-overlay opt-in; the
daemon resolves filesystem authority from its private `0600` registry. CLI
handoffs remain available for initial worktree registration and automation.

Public surfaces expose only opaque `git_store_ref`, `worktree_ref`, `cache_ref`,
commit/blob ids, repository-relative paths, line ranges, and digests. Absolute
paths and source bodies are not part of status, job, diagnostic, or Admin
snapshot contracts. Search responses may contain bounded snippets hydrated
from Git for the authenticated loopback session; those bytes are never written
to the cache or maintenance registry.

The UI state model keeps three lanes separate: registered Git authority enables
offline full-text search, a current ready revision set additionally enables
semantic ranking, and a building/partial set is shown as the active attempt
without hiding the most recent failure class. A failed refresh never hides an
older exact ready set. When no ready set exists, full-text remains enabled and
hybrid requests visibly use lexical fallback. A stale source generation is
shown as a re-registration/rebind requirement rather than silently selecting
another local repository.

## Failure and recovery model

- Invalid committed config: `repository_docs_policy_invalid`; correct and
  commit the policy.
- Missing local object: `git_object_unavailable`; fetch it explicitly outside
  the read path, then retry.
- Provider unavailable: full-text remains available; prepare the configured
  provider and submit a bounded index job for hybrid retrieval.
- Dirty overlay changed: `worktree_overlay_stale` or `superseded`; re-run with
  explicit `--include-worktree`.
- Writer contention: successfully fetched vectors are checkpointed in a
  vector-only durable handoff and replayed without another provider call;
  inspect the active writer and job state when progress is delayed.

No recovery path silently fetches, installs a model, copies document bodies,
or changes repository policy.

Release evidence and manual checks are defined in [Repository documentation RAG
release gates](repository-documentation-rag-release-gates.md).
