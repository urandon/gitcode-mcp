# PR/MR Workflow

This workflow keeps pull request operations cache-first for reads and explicit for remote writes.

## Branches

Use short task-oriented branches, preferably with the `codex/` prefix for agent work:

```sh
git switch -c codex/issue-4-explicit-pr-issue-relation
```

Include the issue number when the branch exists to close or narrow a tracked task. Keep branch names public-safe and avoid private repository names, tracker names, credentials, or raw API payload fragments.

## Creating Pull Requests

Use the CLI write lifecycle for shell workflows:

```sh
gitcode-mcp create-pr \
  --repo YOUR_REPO \
  --title "Implement issue relation API" \
  --body "Summary and tests." \
  --head codex/issue-4-explicit-pr-issue-relation \
  --base main \
  --idempotency-key ik-pr-001
```

`create-pr` runs live by default when credentials and repository binding are available. `--live` remains accepted as a compatibility alias. `create-mr` is an equivalent alias for GitCode UI terminology. Both commands use the same audited service write path and report `command=create-pr`.

Use the MCP write lifecycle for agent workflows:

```json
{
  "repo_id": "YOUR_REPO",
  "write_mode": "live",
  "title": "Implement issue relation API",
  "body": "Summary and tests.",
  "head": "codex/issue-4-explicit-pr-issue-relation",
  "base": "main",
  "idempotency_key": "ik-pr-001"
}
```

Both lifecycles record idempotency, provider confirmation, audit rows, and cache refresh evidence. Direct REST calls are a fallback only when CLI and MCP tools are not available in the current client session.

## Reading Review Discussions

Sync pull requests and their comments before asking for review discussion state:

```sh
gitcode-mcp sync --repo YOUR_REPO --pulls --comments
```

Then list cached review discussions:

```sh
gitcode-mcp pr-discussions --repo YOUR_REPO --number 7 --unresolved-only --format json
```

Create a new inline review discussion through the audited write lifecycle:

```sh
gitcode-mcp add-pr-review-comment \
  --repo YOUR_REPO \
  --number 7 \
  --path internal/service/service.go \
  --line 42 \
  --body "Finding text." \
  --idempotency-key ik-pr-review-001
```

The MCP read tool exposes the same cache-first surface:

```json
{
  "repo_id": "YOUR_REPO",
  "number": 7,
  "unresolved_only": true
}
```

The result groups comments by discussion thread. Inline comments include `path`, `line`, `start_line`, `end_line`, and position fields when GitCode provides them. Schema version 13 exposes the first current diff position as `discussion.position` and all current/original note positions as `comment.positions[]`; those rows can include base/start/head SHAs, old/new paths, old/new lines, line codes, patchset ids, diff ids, and outdated state. General pull request comments are returned with `kind: "general"` and are not mixed with inline review comments. Resolution is tri-state: if GitCode omits `resolved`, unresolved-only reads keep the discussion visible instead of assuming it is resolved.

Writes use GitCode's v5 pull request comments endpoint with an inline payload. Because GitCode can return a sparse create response, the adapter confirms the returned `note_id` or `id`, then stores request-derived inline metadata and a normalized current position for cache-first matching.

The cached position metadata identifies where GitCode placed an inline note. Source-code-change matching should use the local git object database and PR refs or SHAs rather than duplicating PR changed files or diff hunks in SQLite. A matcher can use `base_sha`, `start_sha`, `head_sha`, paths, and lines from `pr_review_positions` as anchors, then resolve surrounding diff/source context through git plumbing as an ephemeral read result.

## Linking Pull Requests To Issues

`link_pr_issue` defaults to `strategy: "auto"`.

In `auto`, the client calls:

```http
POST /api/v5/repos/{owner}/{repo}/pulls/{pr_number}/issues
Content-Type: application/json

[issue_number]
```

The response is expected to be a JSON array of linked issue records. Confirmation requires the returned array to include the requested issue number. Repeating the same link is treated as idempotent when GitCode returns the same linked issue list.

If GitCode reports the relation endpoint as unsupported, the service falls back to updating the PR body with a deterministic marker and `Fixes #N` line:

```text
<!-- gitcode-mcp-link:issue:16 -->
Fixes #16
```

Use `strategy: "description_fallback"` when the caller intentionally wants the body-marker path.

## Merge And Close Caveats

The explicit relation API links the PR/MR and issue, but issue close behavior still depends on GitCode server-side semantics and repository settings. The fallback marker uses `Fixes #N`, which may trigger close-on-merge behavior where GitCode supports it. Agents should mention in PR reports whether the explicit relation API or description fallback was used.
