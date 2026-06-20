# Write Walkthrough

This walkthrough covers the explicit, gated write path for GitCode operations.

## Write safety principles

- All writes require explicit `--dry-run` or `--live` flag.
- `--dry-run` validates the operation without making any mutation.
- `--live` executes the write through the GitCode adapter and records an audit row.
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
  --dry-run
```

Expected: reports what would be created without making any mutation. Cache and remote are unchanged.

### Update issue (dry-run)

```sh
gitcode-mcp update-issue \
  --repo example-owner/example-repo \
  --number 42 \
  --state closed \
  --dry-run
```

Expected: reports what would be updated without making any mutation.

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

## Live mode

Live mode requires:

1. `GITCODE_TOKEN` environment variable set
2. Network access to the GitCode API
3. `--live` flag explicitly passed

### Create issue (live)

```sh
gitcode-mcp create-issue \
  --repo example-owner/example-repo \
  --title "Test issue" \
  --body "Test body." \
  --labels bug \
  --live \
  --idempotency-key "issue-create-001"
```

Expected: issue is created on the remote, audit row is written, cache is refreshed.

### Update issue (live)

```sh
gitcode-mcp update-issue \
  --repo example-owner/example-repo \
  --number 42 \
  --title "Updated title" \
  --state closed \
  --live
```

Expected: issue is updated on remote, audit row recorded, cache refreshed.

### Create wiki page (live)

```sh
gitcode-mcp create-page \
  --repo example-owner/example-repo \
  --slug New-Page \
  --title "New Page" \
  --body "Content." \
  --live
```

Expected: wiki page created on remote, audit row recorded, cache refreshed.

### Add comment (live)

```sh
gitcode-mcp add-comment \
  --repo example-owner/example-repo \
  --kind issue \
  --number 42 \
  --body "Comment text." \
  --live
```

Expected: comment added on remote, audit row recorded, cache refreshed.

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
