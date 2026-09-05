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

Update pull request metadata through the same write lifecycle:

```sh
gitcode-mcp update-pr \
  --repo YOUR_REPO \
  --number 42 \
  --title "Refine issue relation API" \
  --body "Updated summary and test notes." \
  --state open \
  --idempotency-key ik-pr-update-001
```

`update-pr` accepts `--title`, `--body`, and `--state` independently, so callers can update one field without restating the others. Multiline bodies are preserved by the CLI argument parser and passed to the audited service request unchanged.

Merge the reviewed change through the same idempotent write boundary:

```sh
gitcode-mcp merge-pr \
  --repo YOUR_REPO \
  --number 42 \
  --strategy merge \
  --sha EXPECTED_HEAD_SHA \
  --idempotency-key ik-pr-merge-001
```

`merge-pr` accepts `merge`, `squash`, or `rebase`; `merge-mr` is an equivalent alias. The optional `--sha` guard rejects a stale head before mutation. After reading that canonical preimage, the service atomically claims the idempotency key and sends at most one merge PUT. A successful provider response is followed by a PR readback that must report `state=merged`; rerunning against an already merged PR produces an audited idempotent success.

Transport, 5xx, rate-limit, redirect, and readback ambiguity never cause another merge PUT. For this request the adapter disables both its own retry loop and Go's transparent request-body replay, including body-preserving 307/308 redirects. Ambiguous outcomes retain an in-progress fence. Replaying the same key performs only canonical GET recovery and succeeds when the merged PR still has the head SHA captured by the claim; otherwise it returns `write_ambiguous_remote` for operator investigation.

After a canonical merged-state readback, including the no-PUT case where the PR was already merged, the service claims the canonical preimage and the audit first records `remote_confirmed_cache_refresh_pending`, then refreshes the cache, and only then records terminal success. A restart or cache failure in either recovery boundary therefore resumes through GET-only readback and cache repair; it never sends a second PUT.

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
gitcode-mcp sync --repo YOUR_REPO --pulls --pr-comments
```

For a large repository, refresh one already cached pull request without walking other PRs:

```sh
gitcode-mcp sync --repo YOUR_REPO --pr-comments --input pr:7
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

The parent pull request must already be present in the selected cache. This is a preflight safety boundary: when `PR-<number>` is absent, the command returns typed `parent_pr_not_cached` before any remote write. Run the exact remediation and retry with the same idempotency key:

```sh
gitcode-mcp sync --repo YOUR_REPO --pulls --input pr:7
```

Reply to that inline discussion using the stable ids returned by `pr-discussions`:

```sh
gitcode-mcp reply-pr-review-comment \
  --repo YOUR_REPO \
  --number 7 \
  --discussion-id DISCUSSION_ID \
  --parent-comment-id ROOT_COMMENT_ID \
  --body "Confirmed; this is now covered." \
  --idempotency-key ik-pr-review-reply-001
```

The MCP read tool exposes the same cache-first surface:

```json
{
  "repo_id": "YOUR_REPO",
  "number": 7,
  "unresolved_only": true
}
```

The result groups comments by discussion thread. `discussion.id` is a stable presentation id; use `reply_discussion_id` for replies only when `replyable` is true. A synthetic `comment:<root-id>` thread remains readable but reports `replyable: false` and a remediation reason until a provider discussion id is proven. Inline comments include `path`, `line`, `start_line`, `end_line`, and position fields when GitCode provides them. Schema version 13 exposes the first current diff position as `discussion.position` and all current/original note positions as `comment.positions[]`; those rows can include base/start/head SHAs, old/new paths, old/new lines, line codes, patchset ids, diff ids, and outdated state. General pull request comments are returned with `kind: "general"` and are not mixed with inline review comments. Resolution is tri-state: if GitCode omits `resolved`, unresolved-only reads keep the discussion visible instead of assuming it is resolved.

New inline threads use GitCode's v5 pull request comments endpoint. Callers provide a 1-based current-side file `line`; the adapter repeats it in GitCode's `line`, `new_line`, and `position` fields. The legacy public `position` input is a deprecated file-line alias, not a diff-hunk offset, and must equal `line` when both are present. The create lifecycle requires list/discussion readback of the returned note with the requested path and line before it audits or caches success. If GitCode returns a note id but readback is missing or anchored elsewhere, the audit records `remote_confirmed_unsafe`; replaying the same idempotency key is then blocked so it cannot create a duplicate. Replies use the dedicated v5 discussion comments endpoint; the reply lifecycle validates the discussion/root pair, avoids an already matching duplicate, requires readback of the returned note, and refreshes the cached thread before reporting success. Reads combine v5 reply nesting with read-only v4 diff positions because neither representation alone contains both parentage and complete inline anchors.

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
