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
