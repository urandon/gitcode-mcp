# Read Walkthrough

This walkthrough covers the offline, cache-first CLI read flow after repository binding and fixture sync.

## Prerequisites

- `gitcode-mcp` installed ([Install](install.md))
- Repository configured ([Repository Binding](repo-binding.md))
- Fixtures synced and indexed (see below)

## Setup for walkthrough

```sh
# Add the example repository
gitcode-mcp repo add \
  --repo example-owner/example-repo \
  --owner example-owner \
  --name example-repo \
  --scopes issues,wiki \
  --api-base-url https://api.gitcode.com/api/v5

# Sync from the sanitized fixture adapter and build the index
gitcode-mcp sync --offline --repo example-owner/example-repo --issues --wiki --index
```

All subsequent commands work offline from the local cache.

## List sources

```sh
gitcode-mcp list --repo example-owner/example-repo
```

Expected: lists all cached sources (issues, wiki pages) with ids and titles.

Filter by kind:

```sh
gitcode-mcp list --repo example-owner/example-repo --kind issue
gitcode-mcp list --repo example-owner/example-repo --kind wiki
```

## Get a specific source

```sh
gitcode-mcp get --repo example-owner/example-repo issue:42
```

Expected: displays the cached issue body and metadata.

```sh
gitcode-mcp get --repo example-owner/example-repo wiki:Home
```

Expected: displays the cached wiki page body and metadata.

## Search sources

```sh
gitcode-mcp search --repo example-owner/example-repo "remote issue body"
```

Expected: returns sources containing the query text in title or body.

## Get a snippet

```sh
gitcode-mcp get-snippet --repo example-owner/example-repo \
  issue:42 \
  --line-start 1 --line-end 3
```

Expected: returns lines 1-3 of the issue body.

```sh
gitcode-mcp snippet --repo example-owner/example-repo \
  wiki:Home \
  --line-start 1 --line-end 3
```

Expected: returns lines 1-3 of the wiki page body.

## List index chunks

```sh
gitcode-mcp list-chunks --repo example-owner/example-repo
```

Expected: lists all indexed chunks with chunk ids, source references, and offsets.

## Backlinks

```sh
gitcode-mcp backlinks --repo example-owner/example-repo ISSUE-42
```

Expected: lists sources that reference ISSUE-42.

## Link check

```sh
gitcode-mcp link-check --repo example-owner/example-repo
```

Expected: reports unresolved link targets in cached sources.

## Stale index

```sh
gitcode-mcp stale-index --repo example-owner/example-repo
```

Expected: reports sources whose index is missing or out of date.

## Recent changes

```sh
gitcode-mcp recent --repo example-owner/example-repo --limit 5
```

Expected: lists the 5 most recently updated sources.

## Cache status

```sh
gitcode-mcp cache-status --repo example-owner/example-repo
```

Expected: reports cache statistics including record count, index coverage, WAL status, and storage size.

## Sync status

```sh
gitcode-mcp sync-status --repo example-owner/example-repo
```

Expected: reports sync status per source: synced, stale, or missing.

## Export snapshot

```sh
gitcode-mcp export-snapshot --repo example-owner/example-repo --format json
```

Expected: emits a deterministic JSON snapshot of cached records and chunks.

```sh
gitcode-mcp export-snapshot --repo example-owner/example-repo --format markdown
```

Expected: emits a deterministic Markdown export.

## Diff snapshots

```sh
gitcode-mcp diff-snapshot --base-id <snapshot-id-1> --head-id <snapshot-id-2>
```

Expected: reports differences between two snapshots.

## MCP read parity

All CLI read commands have equivalent MCP tools. See [MCP Setup](mcp-setup.md) for tool names and usage.

The MCP tool response for a given query is byte-identical to the CLI output for the same cache state and parameters.
