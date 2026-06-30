# Repository Binding

## Overview

Repository binding is a first-class model in gitcode-mcp. Every cache record, sync event, snapshot, and API call is scoped to a configured repository identified by `repo_id`.

## Adding a repository

For a repo-local cache in the current Git worktree, use the bootstrap command:

```sh
gitcode-mcp repo init-local \
  --repo example-owner/example-repo \
  --owner example-owner \
  --name example-repo \
  --display-name "Example Repository" \
  --api-base-url https://api.gitcode.com/api/v5
```

This creates `.gitcode/gitcode-mcp.yaml` with `cache_mode: repo-local`, ensures `.gitcode/mcp/` is ignored, creates the repo-local cache directory, and records the repository binding in `<git-worktree>/.gitcode/mcp/cache.db`. It does not sync data; run `gitcode-mcp sync --repo example-owner/example-repo ...` explicitly when ready.

To bind a repository to an already selected cache, use `repo add`:

```sh
gitcode-mcp repo add \
  --repo example-owner/example-repo \
  --owner example-owner \
  --name example-repo \
  --display-name "Example Repository" \
  --scopes issues,wiki \
  --api-base-url https://api.gitcode.com/api/v5
```

### Flags

| Flag | Required | Description |
|---|---|---|
| `--repo` | Yes | Stable local repo id (`owner/name`) |
| `--owner` | Yes | Repository owner/namespace |
| `--name` | Yes | Repository name |
| `--display-name` | No | Human-readable display name |
| `--scopes` | Yes | Comma-separated scopes (`issues`, `wiki`; `pulls` and `comments` are accepted and use the issue-backed GitCode API surface) |
| `--api-base-url` | No | API base URL. Defaults to config value |
| `--alias` | No | Short alias for the repository |

`repo init-local` accepts the same repository identity flags. Its `--scopes` default is `issues,wiki,pulls,comments`, and `--overwrite` replaces an existing `.gitcode/gitcode-mcp.yaml` when it already declares a different `cache_mode`.

### repo_id format

The stable local `repo_id` is formed as `<owner>/<name>`. For example, `example-owner/example-repo`.

## Viewing repository status

```sh
gitcode-mcp repo status --repo example-owner/example-repo
```

Shows status for a specific repository, including scopes, aliases, and metadata.

## Scope resolution

Issues and wiki pages are resolved within repository scope:

```sh
# Issue by alias (repo-scoped)
gitcode-mcp get --repo example-owner/example-repo issue:42

# Wiki page by alias (repo-scoped)
gitcode-mcp get --repo example-owner/example-repo wiki:Home
```

The same `issue:42` or `wiki:Home` identifier will not collide across different repositories because each cache query carries `repo_id`.

## Alias collision

If two repositories have the same alias, resolution is ambiguous and the command returns an error. Use the full `repo_id` to disambiguate.
