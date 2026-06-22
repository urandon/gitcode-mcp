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
2. keychain credential source when available;
3. none.

With no token, `auth status` reports `token_present: false`, lists `available_sources` in the env, keychain, none order, and must not print raw secrets.

To include a live auth probe:

```sh
gitcode-mcp auth status --live --owner "YOUR_OWNER" --repo "YOUR_REPO"
```

`auth status --help` documents `--live`, `--owner`, `--repo`, and `--format`. The command reports token source, credential state, and optional auth probe with redacted output.

## 4. Live sync

Default sync uses the configured offline/fixture provider unless live mode is explicitly requested.

Run live sync with the command-local live selector:

```sh
gitcode-mcp sync --live --repo "YOUR_REPO" --issues --wiki --index
```

`sync --help` documents `--live` as the live GitCode API provider selector for sync, plus `--repo`, `--issues`, `--wiki`, `--index`, `--id`, `--input`, `--idempotency-key`, `--cache-path`, and `--format`.

Live sync fetches issue records, comments, and wiki pages through the live GitCode provider. Fetches are page/resource scoped; successful records are committed to cache, failures are collected and reported, and re-sync should report deltas rather than duplicate records. Auth failures and rate limits are reported as diagnostics instead of raw API payloads.

## 5. Live write

Write commands require exactly one of `--dry-run` or `--live`.

Execute a live issue create only after credentials and binding are ready:

```sh
gitcode-mcp create-issue --live --idempotency-key "ik-001" --title "Test"
```

Include an issue body when needed:

```sh
gitcode-mcp create-issue --live --idempotency-key "ik-001" --title "Test" --body "Body"
```

Validate without mutating the remote by using dry-run instead of live:

```sh
gitcode-mcp create-issue --dry-run --idempotency-key "ik-001" --title "Test" --body "Body"
```

`create-issue --help` documents `--live`, `--dry-run`, `--idempotency-key`, `--title`, and `--body`. `--live` executes the live write, `--dry-run` validates without mutation, `--idempotency-key` supports audited retries, `--title` is required, and `--body` supplies the issue body.

## 6. Search

After sync and indexing, search cached source records:

```sh
gitcode-mcp search_sources "query"
```

`search_sources --help` documents `--repo`, `--kind`, `--limit`, `--offset`, `--cache-path`, and `--format`. The `--kind` filter includes `issue` and `wiki`. A query with no matches should return an empty result set, not a cache-empty error after successful sync/index.

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

Include live checks only when credentials and network access are intentionally available:

```sh
gitcode-mcp doctor --repo "YOUR_REPO" --live
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
| Rate limited | `gitcode-mcp sync --live --repo "YOUR_REPO" --issues --wiki` | Retry later; keep cache reads offline |
| Missing or stale index | `gitcode-mcp sync-status --repo "YOUR_REPO"` and `gitcode-mcp stale-index --repo "YOUR_REPO"` | Run `gitcode-mcp index --repo "YOUR_REPO" --full` |
| MCP not ready | HTTP/SSE `/ready` or `gitcode-mcp doctor` | Confirm cache path and repository binding |

Keep all examples sanitized. Do not place raw tokens, private repository coordinates, Authorization headers, cookies, internal URLs, or raw API responses in docs, fixtures, logs, or handoffs.
