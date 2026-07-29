# Write Walkthrough

This walkthrough covers the explicit, gated write path for GitCode operations.

## Write safety principles

- Writes execute live by default for configured repositories.
- `--dry-run` validates the operation without making any mutation.
- `--live` remains accepted as a compatibility alias for live writes.
- No write can succeed without reaching the remote adapter.
- Idempotency keys prevent duplicate writes.

## Dry-run mode

All write commands support `--dry-run` for pre-flight validation.

### Create issue (dry-run)

```sh
gitcode-mcp create-issue \
  --repo example-owner/example-repo \
  --title "Test issue" \
  --body "This is a test issue body." \
  --labels bug,needs-triage \
  --milestone MILESTONE-1 \
  --dry-run
```

Expected: reports what would be created without making any mutation. Cache and remote are unchanged.

### Update issue (dry-run)

```sh
gitcode-mcp update-issue \
  --repo example-owner/example-repo \
  --number 42 \
  --state closed \
  --clear-milestone \
  --dry-run
```

Expected: reports what would be updated without making any mutation.
`--milestone ID_OR_TITLE` assigns a milestone; `--clear-milestone` clears it,
and the two flags are mutually exclusive.

### Create pull request / merge request (dry-run)

```sh
gitcode-mcp create-pr \
  --repo example-owner/example-repo \
  --title "Add cache-first PR flow" \
  --body "Summary and tests." \
  --head feature-branch \
  --base main \
  --dry-run
```

Expected: reports what would be created without making any mutation. `create-mr` is an alias for users who follow GitCode UI terminology.

### Milestones (dry-run)

```sh
gitcode-mcp create-milestone \
  --repo example-owner/example-repo \
  --title "RAG indexer MVP" \
  --description "Implementation milestone" \
  --due-on 2026-07-15 \
  --dry-run
```

Expected: validates milestone creation without mutation. GitCode requires `--due-on` for milestone creation.

```sh
gitcode-mcp set-issue-milestone \
  --repo example-owner/example-repo \
  --number 42 \
  --milestone "RAG indexer MVP" \
  --dry-run
```

Expected: validates issue milestone assignment. Live `set-issue-milestone` and `clear-issue-milestone` verify the result through issue readback because GitCode can return a stale or null milestone in the immediate PATCH response.

The generic `create-issue` and `update-issue` commands use the same resolver and
readback contract. A milestone selector may be a numeric remote id, stable
`MILESTONE-<id>`, or exact title. Live receipts include the resolved stable id,
remote id, and title; clear operations include an explicit cleared marker.

### Create wiki page (dry-run)

```sh
gitcode-mcp create-page \
  --repo example-owner/example-repo \
  --slug New-Page \
  --title "New Wiki Page" \
  --body "Page content here." \
  --dry-run
```

Expected: reports what would be created.

### Add comment (dry-run)

```sh
gitcode-mcp add-comment \
  --repo example-owner/example-repo \
  --kind issue \
  --number 42 \
  --body "This is a test comment." \
  --dry-run
```

Expected: reports what would be added.

### Update comment (dry-run)

```sh
gitcode-mcp update-comment \
  --repo example-owner/example-repo \
  --comment-id 2002 \
  --number 42 \
  --body $'Updated comment\nwith real Markdown newlines.' \
  --dry-run
```

Expected: reports what would be updated. `--number` is optional for the live GitCode route, but it helps the local cache resolve the parent issue deterministically.

## Live mode

Live mode is the default for write commands and requires:

1. `GITCODE_TOKEN` environment variable set
2. Network access to the GitCode API
3. no explicit `--dry-run`

### Create issue (live)

```sh
gitcode-mcp create-issue \
  --repo example-owner/example-repo \
  --title "Test issue" \
  --body "Test body." \
  --labels bug \
  --idempotency-key "issue-create-001"
```

Expected: issue is created on the remote, audit row is written, cache is refreshed.

### Update issue (live)

```sh
gitcode-mcp update-issue \
  --repo example-owner/example-repo \
  --number 42 \
  --title "Updated title" \
  --state closed
```

Expected: issue is updated on remote, audit row recorded, cache refreshed.

The public CLI and MCP state values are `open` and `closed`. The GitCode adapter translates them to the write-only transition events `reopen` and `close`, then requires issue readback with the requested public state. A state-only update does not send title, body, labels, milestone, or assignee fields.

### Create pull request / merge request (live)

```sh
gitcode-mcp create-pr \
  --repo example-owner/example-repo \
  --title "Add cache-first PR flow" \
  --body "Summary and tests." \
  --head feature-branch \
  --base main \
  --idempotency-key "pr-create-001"
```

Expected: pull request is created on remote, audit row recorded, cache refreshed. Use `create-mr` as an equivalent alias when matching GitCode UI language.

### Create wiki page (live)

```sh
gitcode-mcp create-page \
  --repo example-owner/example-repo \
  --slug New-Page \
  --title "New Page" \
  --body "Content."
```

Expected: wiki page created on remote, audit row recorded, cache refreshed.

### Add comment (live)

```sh
gitcode-mcp add-comment \
  --repo example-owner/example-repo \
  --kind issue \
  --number 42 \
  --body "Comment text."
```

Expected: comment added on remote, audit row recorded, cache refreshed.

### Update comment (live)

```sh
gitcode-mcp update-comment \
  --repo example-owner/example-repo \
  --comment-id 2002 \
  --number 42 \
  --body $'Updated comment\nwith real Markdown newlines.'
```

Expected: existing issue comment updated on remote through `PATCH /api/v5/repos/{owner}/{repo}/issues/comments/{comment_id}`, audit row recorded, and the cached `record_comments` row upserted instead of duplicated.

## Idempotency

Idempotency keys prevent duplicate writes. If a write with the same key is retried:

- The audit trail shows the prior successful write.
- A duplicate is not created on the remote.
- The command reports success and references the prior audit row.

## Error handling

Write failures produce typed errors:

| Error class | Description |
|---|---|
| `adapter_unavailable` | GitCode adapter cannot process the request (no token, no network) |
| `remote_error` | Remote API returned an error |
| `conflict` | Remote state conflicts with the requested change |
| `audit_failure` | Write succeeded on remote but audit row could not be recorded |
| `validation_error` | Request parameters are invalid |

The command exit code reflects the error class. Error messages do not expose tokens or private data.

## Fixture-mode write walkthrough

When running without live credentials, write commands in `--dry-run` mode validate against the fixture cache without network access. This is the default behavior for the docs smoke tests.
