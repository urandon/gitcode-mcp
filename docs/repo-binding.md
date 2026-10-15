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
  --display-name "Example Repository"
```

This creates `.gitcode/gitcode-mcp.yaml` with `cache_mode: repo-local`, ensures `.gitcode/mcp/` is ignored, creates the repo-local cache directory, and records the repository binding in `<git-worktree>/.gitcode/mcp/cache.db`. It does not sync data; run `gitcode-mcp sync --repo example-owner/example-repo ...` explicitly when ready.

To bind a repository to an already selected cache, use `repo add`:

```sh
gitcode-mcp repo add \
  --repo example-owner/example-repo \
  --owner example-owner \
  --name example-repo \
  --display-name "Example Repository" \
  --scopes issues,wiki
```

### Flags

| Flag | Required | Description |
|---|---|---|
| `--repo` | Yes | Stable local repo id (`owner/name`) |
| `--owner` | Yes | Repository owner/namespace |
| `--name` | Yes | Repository name |
| `--display-name` | No | Human-readable display name |
| `--scopes` | Yes | Comma-separated scopes (`issues`, `wiki`; `pulls` and `comments` are accepted and use the issue-backed GitCode API surface) |
| `--api-base-url` | No | API base URL. Defaults to effective `gitcode_base_url`, then `https://api.gitcode.com/api/v5` |
| `--alias` | No | Short alias for the repository |

`repo init-local` accepts the same repository identity flags. Its `--scopes` default is `issues,wiki,pulls,comments`, and `--overwrite` replaces an existing `.gitcode/gitcode-mcp.yaml` when it already declares a different `cache_mode`.

`repo init-local` always bootstraps the worktree cache, even when defaults,
environment variables, or global configuration select another cache for routine
commands. Omit `--cache-path`; an explicitly supplied nonempty path (including
a global flag before `repo`) is rejected before bootstrap writes. The legacy
empty `--cache-path ''` workaround after the command remains accepted but is
unnecessary. After bootstrap, normal cache-selection precedence still applies
to other commands.

For a GitCode-compatible deployment at another endpoint, set `gitcode_base_url` in configuration or pass an explicit override:

```sh
gitcode-mcp repo add \
  --repo example-owner/example-repo \
  --owner example-owner \
  --name example-repo \
  --scopes issues,wiki \
  --api-base-url https://gitcode.example/api/v5
```

The explicit flag takes precedence over configuration. An invalid explicit URL fails validation; it does not fall back silently.

The deprecated `bind --repo-owner OWNER --repo REPO` form remains a working compatibility alias. It derives the stable id as `OWNER/REPO`, defaults scopes to all supported collections, and uses the same API URL precedence as `repo add`. New automation should use `repo add` because its repository identity is explicit.

### repo_id format

The stable local `repo_id` is formed as `<owner>/<name>`. For example, `example-owner/example-repo`.

## Updating scopes on an existing binding

`repo add` remains add-only: a duplicate identity is a conflict, not an implicit
update. To replace scopes without a coordinator service:

```sh
gitcode-mcp repo set-scopes \
  --repo example-owner/example-repo \
  --scopes issues,wiki \
  --format json
```

Use the exact stored `repo_id`, not a short alias. `--scopes` is a replacement,
not an addition; include every scope you want to retain. Shared validation
normalizes `pull`, `pulls`, `pr`, `prs`, `pull_request`, `pull_requests`, `comment`
and `comments` to the issue-backed `issues` scope. Only `issues` and `wiki` are
persisted, in that order. Empty and unknown scopes are errors.

The command takes the cache writer lease and changes only scopes plus the
binding's update timestamp, then reads back the binding. Identity, custom API
route, aliases, display name, cache identity, configuration bytes/cache mode,
and cached records are preserved. Reducing scopes does not delete cached data.
Repeating the same scope selection is safe; it does not recreate the binding.
No credential, daemon installation, GitCode request, remote write, or implicit
sync is required. Normal cache-selection precedence applies; `--cache-path`
can explicitly select the existing cache. No `--live` or `--dry-run` is used.
Identity/endpoint/alias flags are rejected rather than silently ignored.

The existing Admin binding plan/apply controls remain the UI equivalent and
support full metadata updates. MCP binding mutation remains intentionally
unavailable. Binding scopes select provider collections; they are not per-agent
tool permissions or a substitute for GitCode authorization.

## Viewing repository status

```sh
gitcode-mcp repo status --repo example-owner/example-repo
```

Shows status for a specific repository, including scopes, aliases, and metadata.

## Explicit live bootstrap verification

`repo status` is cache-only binding/runtime status, not proof of provider
identity or private visibility. When authorized to verify the destination:

```sh
gitcode-mcp repo-metadata --repo example-owner/example-repo --format json
gitcode-mcp list-repo-labels --repo example-owner/example-repo --format json
gitcode-mcp create-repo-label --repo example-owner/example-repo \
  --name 'state:ready' --color '#0f766e' --live \
  --idempotency-key bootstrap-label-ready --format json
```

Metadata reports canonical `provider_id`, `owner`, `name`, `full_name`, `private`
and `default_branch`, with an observation timestamp. No binding is changed and
visibility is not a lease authorizing subsequent writes. Label listing is
explicitly live and complete within bounded pagination; failure cannot establish
absence. Standalone creation requires `--live` or non-mutating `--dry-run` and a
caller key. It never assigns issues, recolors existing names, replaces all
labels, or writes descriptions. Keep the same key after ambiguity or cache
publication failure: reconciliation uses canonical GET only, never another POST.
Descriptions returned by GitCode are readable but their write transport is not
qualified. Cached `LABEL-<provider-id>` records can be read offline like other
sources. Remote editing is intentionally absent from the local Admin console.

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
