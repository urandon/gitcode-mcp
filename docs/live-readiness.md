# Live Readiness Operator Guide

This guide walks an operator through live-readiness setup for `gitcode-mcp` while preserving cache-first default behavior and public-safe output. Examples use only sanitized placeholders: `YOUR_OWNER`, `YOUR_REPO`, `$GITCODE_TOKEN`, `[REDACTED]`, and `ik-001`.

## 1. Prerequisites

Install or build `gitcode-mcp`, then choose local cache and config locations:

```sh
export GITCODE_MCP_CACHE_DIR="/path/to/gitcode-mcp-cache"
export GITCODE_MCP_CONFIG_DIR="/path/to/gitcode-mcp-config"
export GITCODE_TOKEN="$GITCODE_TOKEN"
```

`GITCODE_TOKEN` is the portable credential baseline for live operations. See [Secrets](secrets.md) for token setup and redaction expectations, and [Repository Binding](repo-binding.md) for repository binding details.

Check command discovery:

```sh
gitcode-mcp --help
```

## 2. Bind a repo

Bind the repository with sanitized owner and repository placeholders:

```sh
gitcode-mcp bind --repo-owner "YOUR_OWNER" --repo "YOUR_REPO"
```

`bind --help` documents `--repo-owner` and `--repo`. The current implementation also exposes repository binding through `repo add`; see [Repository Binding](repo-binding.md) for the detailed repository management workflow.

## 3. Verify credentials

Check credential state without printing the token:

```sh
gitcode-mcp auth status
```

The credential pipeline is:

1. `GITCODE_TOKEN` environment variable;
2. system keyring credential source when available;
3. none.

With no token, `auth status` reports `token_present: false`, lists `available_sources` in the env, keyring, none order, and must not print raw secrets.

To include a live auth probe:

```sh
gitcode-mcp auth status --live --owner "YOUR_OWNER" --repo "YOUR_REPO"
```

`auth status --help` documents `--live`, `--owner`, `--repo`, and `--format`. The command reports token source, credential state, and optional auth probe with redacted output.

## 4. Live sync

Sync uses the configured live provider by default. Use `--offline` or `--fixture` only when deterministic fixture data is needed for docs smoke or tests.

Run live sync:

```sh
gitcode-mcp sync --repo "YOUR_REPO" --issues --wiki --index
```

`sync --help` documents `--offline` and `--fixture` as explicit fixture selectors, and `--live` as a compatibility alias, plus `--repo`, `--issues`, `--wiki`, `--pulls`, `--comments`, `--index`, `--id`, `--input`, `--idempotency-key`, `--max-pages`, `--max-records`, `--per-page`, `--details`, `--records`, `--cache-path`, and `--format`.

Live sync fetches issue records, comments, and wiki pages through the live GitCode provider. Fetches are page/resource scoped; successful records are committed to cache, failures are collected and reported, and re-sync should report deltas rather than duplicate records. Issue collection sync uses list-level issue revisions before comment-list fetches, so an unchanged issue can report `skipped_by_revision` and avoid listing comments again. Wiki collection sync uses list-level page revisions before body fetches, so an unchanged page can report `skipped_by_revision` and avoid a full page-body request. Auth failures and rate limits are reported as diagnostics instead of raw API payloads.

For large repositories, bound collection work explicitly:

```sh
gitcode-mcp --timeout 30s sync --repo "YOUR_REPO" --issues --max-pages 3 --per-page 50
```

The startup `--timeout` value bounds the whole CLI operation context. Collection bounds limit list traversal and record commits. During bulk sync, progress lines are written to stderr with the current collection, page, committed record count, and elapsed time. If the operation times out or is cancelled after some records are written, the command reports partial counts, grouped failures, elapsed time, and typed diagnostics while keeping successful records in the cache.

Bulk sync text output defaults to an aggregate summary:

```sh
sync progress: collection=issues page=1 committed=100 elapsed=2.4s
sync: succeeded success_count=100 failure_count=0 fetched=100 updated=0 inserted=0 skipped=100 conflicts=0 listed=100 fetched_detail=0 skipped_by_revision=100 zero_delta=100 elapsed=2.5s pages_listed=1 records_listed=100 skipped_by_watermark=0 stop_reason=watermark
```

Use `--format json` to inspect the same compact summary structurally for automation checks. Use `--details` or `--records` when per-record sync evidence is required; without that flag, bulk `sync --format json` and aggregate `sync-status --format json` omit large `results[]` arrays.

## 5. Live write

Write commands execute live by default. Use `--dry-run` to validate without mutating the remote.

Execute a live issue create only after credentials and binding are ready:

```sh
gitcode-mcp create-issue --idempotency-key "ik-001" --title "Test"
```

Include an issue body when needed:

```sh
gitcode-mcp create-issue --idempotency-key "ik-001" --title "Test" --body "Body"
```

Validate without mutating the remote by using dry-run:

```sh
gitcode-mcp create-issue --dry-run --idempotency-key "ik-001" --title "Test" --body "Body"
```

`create-issue --help` documents `--live`, `--dry-run`, `--idempotency-key`, `--title`, and `--body`. `--dry-run` validates without mutation, `--idempotency-key` supports audited retries, `--title` is required, and `--body` supplies the issue body.

Create a pull request through the same audited CLI write lifecycle:

```sh
gitcode-mcp create-pr --repo "YOUR_REPO" --idempotency-key "ik-pr-001" --title "Test PR" --head "feature-branch" --base "main" --body "Body"
```

`create-pr --help` documents `--live`, `--dry-run`, `--idempotency-key`, `--title`, `--body`, `--head`, and `--base`. `create-mr` is an alias for GitCode UI terminology and uses the same service write path.

The MCP server exposes the same audited live-write lifecycle for agent workflows that previously needed shell or direct REST fallback:

| MCP read tool | Use |
|---|---|
| `list_pr_discussions` | List cached PR review discussions, including unresolved-only filtering and inline path/line metadata when available |

| MCP tool | Use |
|---|---|
| `add_issue_comment` | Add a proposal or status comment to an issue |
| `update_issue_comment` | Update an existing issue comment body |
| `update_issue` | Update issue title, body, state, or labels |
| `create_pr` | Create a pull request with title, body, head, and base |
| `update_pr` | Update pull request title, body, or state |
| `add_pr_comment` | Add a testing/report comment to a pull request |
| `add_pr_review_comment` | Create an inline pull request review comment on a changed file line or diff position |
| `link_pr_issue` | Link a pull request to an issue through the GitCode relation API with fallback |

MCP tool access defaults to `read`, so these write lifecycle tools are hidden from `tools/list` unless the server is started with `mcp.tools.access: write` or `GITCODE_MCP_TOOL_ACCESS=write`. A direct call while read-only returns `tool_disabled_by_policy` before validation, credentials, network, or cache mutation. `gitcode-mcp doctor` reports the active `tool_access` mode.

Each MCP write call also requires `write_mode: "live"`. Idempotency keys are accepted on every write tool and are used for safe replay/conflict detection. `link_pr_issue` defaults to `strategy: "auto"`, which uses the explicit GitCode PR issue relation endpoint and falls back to a deterministic description marker only when the relation endpoint is unsupported. Successful writes report provider confirmation, audit/cache evidence, remote identity, and the idempotency key; failed writes return typed public-safe diagnostics and do not claim cache confirmation.

## 6. Search

After sync and indexing, search cached source records:

```sh
gitcode-mcp search_sources "query"
```

`search_sources --help` documents `--repo`, `--kind`, `--provenance`, `--limit`, `--offset`, `--cache-path`, and `--format`. The `--kind` filter includes `issue` and `wiki`; the `--provenance` filter includes `live`, `fixture`, `remote`, `projection`, and `bridge`. A query with no matches should return an empty result set, not a cache-empty error after successful sync/index.

Chunk search remains available separately:

```sh
gitcode-mcp search-chunks "query"
```

## 7. MCP server

Start stdio MCP mode:

```sh
gitcode-mcp --mcp --cache-path "/path/to/cache.db"
```

Equivalent explicit form:

```sh
gitcode-mcp mcp serve --transport stdio --cache-path "/path/to/cache.db"
```

Start HTTP/SSE mode:

```sh
gitcode-mcp mcp serve --transport http-sse --bind 127.0.0.1:9020 --cache-path "/path/to/cache.db"
```

See [MCP Setup](mcp-setup.md) for transport details, health/readiness endpoints, and client examples.

The seven read tools used for live-readiness validation are:

| Tool | Purpose |
|---|---|
| `cache_status` | Report cache storage and index-warning status |
| `list_sources` | List cached source records |
| `get_source` | Read one cached source record |
| `sync_status` | Report sync state |
| `list_chunks` | List indexed chunks |
| `search_chunks` | Search indexed chunks |
| `search_sources` | Search cached source records |

Where kind filters are exposed, `issue` and `wiki` are valid source kinds.

## 8. Doctor diagnostics

Run doctor for a public-safe readiness report:

```sh
gitcode-mcp doctor
```

For a specific repository:

```sh
gitcode-mcp doctor --repo "YOUR_REPO"
```

Force deterministic fixture diagnostics when credentials or network access are intentionally unavailable:

```sh
gitcode-mcp doctor --offline --repo "YOUR_REPO"
```

Doctor output covers binary version, config path, cache path, cache schema version, repository binding status, token source and credential status, provider mode, auth probe status when requested, last sync, index freshness, and MCP transport health. Sensitive values are redacted.

With no repository binding, doctor reports `status: no_repo_bound` and suggests the `bind` command.

## 9. Troubleshooting

See [Troubleshooting](troubleshooting.md) for common diagnostics and fixes.

Quick checks:

| Symptom | Check | Fix |
|---|---|---|
| No token configured | `gitcode-mcp auth status` | Set `GITCODE_TOKEN` or configure a credential store from [Secrets](secrets.md) |
| No repo bound | `gitcode-mcp doctor` | Run `gitcode-mcp bind --repo-owner "YOUR_OWNER" --repo "YOUR_REPO"` |
| Auth failure | `gitcode-mcp auth status --live --owner "YOUR_OWNER" --repo "YOUR_REPO"` | Verify token scope and repository access |
| Rate limited | `gitcode-mcp sync --repo "YOUR_REPO" --issues --wiki` | Retry later; keep cache reads offline |
| Missing or stale index | `gitcode-mcp sync-status --repo "YOUR_REPO"` and `gitcode-mcp stale-index --repo "YOUR_REPO"` | Run `gitcode-mcp index --repo "YOUR_REPO" --full` |
| MCP not ready | HTTP/SSE `/ready` or `gitcode-mcp doctor` | Confirm cache path and repository binding |

Keep all examples sanitized. Do not place raw tokens, private repository coordinates, Authorization headers, cookies, internal URLs, or raw API responses in docs, fixtures, logs, issue comments, pull request reports, or wiki pages.
