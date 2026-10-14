# Local Git project format (schema 1)

This is the internal foundation of epic #200, not a currently enabled CLI,
MCP or Admin feature. Its child PRs integrate into
`codex/epic-200-git-backed-harness`; the whole cohort enters `main` together.
Git owns all project content and history. No cache database is required by the
reader, and no source or chunk bytes are copied into an index by this package.

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

Unknown/duplicate fields, aliases, anchors, unsupported schemas, invalid IDs,
identity/path mismatches, binary/LFS/symlink/submodule entity files and malformed
metadata are rejected. Titles are at most 512 UTF-8 bytes, labels at most 32
unique values of 128 bytes, milestone at most 128 bytes, links at most 64.

Receipts contain only `schema`, `project_id`, `key_hash`, `intent_hash`,
`operation`, typed `result`, and `result_blob`. Hashes are 64 lowercase hex
characters; the result blob is a full Git object ID. Operation must match the
result kind. Unknown/duplicate keys and content fields are rejected. A receipt
does not contain its own producing commit ID: Git history supplies that locator.
Publication/replay is the next adapter child, not implemented by the reader.

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
history. A prefix too short to prove complete history returns
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

Reader tests create real temporary Git stores without credentials or network.
CLI/MCP exposure, writes, service hosting, search/RAG, protected Admin and bundle
restore remain separate required children of #200.
