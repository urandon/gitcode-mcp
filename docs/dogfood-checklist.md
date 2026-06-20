# Dogfood Checklist

One-week implementation checklist for the GitCode MCP minimum dogfood slice. Each day has an executable product check culminating in offline CLI and MCP reads for one fixture issue and one fixture wiki page.

## Prerequisites

- `gitcode-mcp` binary built or available via `go run ./cmd/gitcode-mcp`
- Fixture config at `testdata/configs/dogfood.yaml`
- Fixture allowlist at `project/dogfood/fixture-allowlist.txt`
- Shared safety policy at `project/dogfood/lib/safety.sh`
- No live credentials required (all slices run fixture-only by default)

## Day Slices

| Slice ID | Name | Required Prior | Description |
|---|---|---|---|
| `day1-config-repo` | Config & Repository Binding | (none) | Verify config discovery, redacted display, repo add, and repo status |
| `day2-fixture-sync-index` | Fixture Sync & Index | `day1-config-repo` | Sync fixture issues/wiki and build the index |
| `day3-cli-reads` | CLI Reads | `day2-fixture-sync-index` | Offline CLI reads for one issue and one wiki page |
| `day4-mcp-parity-transport` | MCP Parity & Transport | `day3-cli-reads` | MCP read over HTTP/SSE fixture cache, issue and wiki coverage |
| `day5-concurrency-write-safety` | Concurrency & Write Safety | `day4-mcp-parity-transport` | Documented concurrency/write-safety product check or diagnostic |
| `day6-snapshot-integrity` | Snapshot Integrity | `day5-concurrency-write-safety` | Export/diff snapshot product check or documented diagnostic |
| `day7-docs-live-validation-feedback` | Docs, Live Validation & Feedback | `day6-snapshot-integrity` | Docs smoke plus fixture validation; live skipped without credentials |

## Running the Checklist

```sh
# Run a single slice
project/dogfood/the\ run.sh \
  --slice day1 \
  --cache-path /tmp/gitcode-mcp.db \
  --transcript project/dogfood/evidence/day1.md

# Run all slices in order (day1 through day7)
for day in day1 day2 day3 day4 day5 day6 day7; do
  project/dogfood/the\ run.sh \
    --slice "${day}" \
    --cache-path /tmp/gitcode-mcp.db \
    --transcript "project/dogfood/evidence/${day}.md"
done
```

## Slice Details

### Day 1: Config & Repository Binding

Commands:
- `--version` — print version
- `--help` — print help
- `config locate` — locate active config path
- `config show --redacted` — display config with redacted credentials
- `config show` — display full config (paths redacted in transcript)
- `auth status` — show auth status
- `repo add --repo example-owner/example-repo --owner example-owner --name example-repo --scopes issues,wiki --api-base-url https://api.gitcode.com/api/v5 --display-name "Example Repository" --alias example`
- `repo status --repo example-owner/example-repo` — verify repository binding

Product check: All commands succeed or return documented diagnostics. Repository is bound and visible in `repo status`.

### Day 2: Fixture Sync & Index

Commands:
- `sync --repo example-owner/example-repo --issues --wiki --index` — sync from fixture adapter and build index
- `cache-status --repo example-owner/example-repo` — verify cache populated
- `sync-status --repo example-owner/example-repo` — verify sync events recorded

Product check: Cache contains fixture records. Index chunks exist. Sync events are recorded.

### Day 3: CLI Reads

Commands (offline, cache-first):
- `list --repo example-owner/example-repo` — list all sources
- `get --repo example-owner/example-repo issue:42` — read issue body
- `get --repo example-owner/example-repo wiki:Home` — read wiki page body
- `search --repo example-owner/example-repo "remote issue body"` — full-text search
- `get-snippet --repo example-owner/example-repo issue:42 --line-start 1 --line-end 3` — snippet from issue
- `snippet --repo example-owner/example-repo wiki:Home --line-start 1 --line-end 3` — snippet from wiki
- `list-chunks --repo example-owner/example-repo` — list index chunks
- `backlinks --repo example-owner/example-repo ISSUE-42` — backlinks from other sources
- `link-check --repo example-owner/example-repo` — link integrity
- `stale-index --repo example-owner/example-repo` — stale index report
- `recent --repo example-owner/example-repo --limit 5` — recent changes
- `cache-status --repo example-owner/example-repo` — cache statistics

Product check: All read commands return repo-scoped, deterministic output. Issue `ISSUE-42` and wiki `wiki:Home` are readable.

### Day 4: MCP Parity & Transport

Commands:
- Start HTTP/SSE MCP server against fixture cache
- Verify `/health` returns 200
- Verify `/ready` returns readiness state
- POST `tools/call` for `get_snippet` (ISSUE-42, lines 1-3) via `/message` endpoint
- POST `tools/call` for `get_snippet` (wiki:Home, lines 1-3) via `/message` endpoint

Product check: MCP server starts. Health and readiness endpoints respond. At least one MCP read tool returns documented fixture snippet or documented diagnostic.

### Day 5: Concurrency & Write Safety

Commands:
- `create-issue --repo example-owner/example-repo --title "Dry run test" --body "Dry run body." --dry-run` — verify no mutation
- `create-issue --repo example-owner/example-repo --title "Live gated" --body "Live gated body." --live --idempotency-key day5-live-gated` — expected live_skip or documented_diagnostic
- `update-issue --repo example-owner/example-repo --id ISSUE-42 --title "Updated" --dry-run` — verify no mutation
- `create-page --repo example-owner/example-repo --slug test-page --title "Test Page" --body "Test body." --dry-run` — verify no mutation
- `add-comment --repo example-owner/example-repo --id ISSUE-42 --body "Fixture comment." --dry-run` — verify no mutation

Product check: All `--dry-run` commands return dry_run confirmation without mutation. `--live` commands are skipped or return documented diagnostic when credentials are absent.

### Day 6: Snapshot Integrity

Commands:
- `export-snapshot --repo example-owner/example-repo --format json` — export JSON snapshot
- `export-snapshot --repo example-owner/example-repo --format markdown` — export Markdown snapshot
- `diff-snapshot --base-id <snapshot-id-1> --head-id <snapshot-id-2>` — diff two snapshots (expected: documented_diagnostic or not-found on invalid IDs)

Product check: Export produces deterministic output. Diff rejects unknown IDs with not-found or returns documented diagnostic.

### Day 7: Docs, Live Validation & Feedback

Commands:
- Run `project/dogfood/docs-smoke.sh` with fixture config and cache
- Run `project/dogfood/validate-fixtures.sh` with fixture path
- Verify offline MCP reads for `ISSUE-42` and `wiki:Home`
- Live validation skipped without credentials

Product check: Docs smoke passes. Fixture validation passes offline. Offline CLI and MCP reads for one fixture issue and one fixture wiki page succeed.

## Pass Criteria

- Each day slice produces a redacted transcript under `project/dogfood/evidence/`
- Transcripts contain no credentials, private paths, cookies, raw live responses, or non-allowlisted repo identifiers
- Day 7 final evidence includes offline CLI and MCP reads for fixture issue `ISSUE-42` and fixture wiki `wiki:Home`
- Replacement commands (when documented commands change) are recorded as append-only evidence entries

## Replacement-Command Rules

If a documented command name or flag changes during implementation:
1. Append a new evidence entry with `replaces_command_id`, `reason`, and `supersedes_transcript`
2. Never edit or delete prior evidence entries
3. A slice can be marked complete only when its product check passes and all prior slices either pass or have a documented replacement command
