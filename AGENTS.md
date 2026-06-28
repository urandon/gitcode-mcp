# Agent Guide

This repository is the implementation home for cache-first GitCode MCP/CLI tooling.

Mission: make GitCode workable for agents and humans under poor network availability by keeping a durable local cache, deterministic exports, link/id resolution, and MCP access over cached data.

## Boundary

Keep this repository self-contained and public-safe.

Use this repo for:

- code, tests, fixtures, cache schema, CLI, and MCP server work;
- durable GitCode API compatibility notes and captured fixtures;
- technical documentation that is part of the product surface.

Use GitCode issues and pull requests for active planning and handoffs. Use the GitCode wiki for historical research, decisions, and dogfood evidence that should remain discoverable without living in main.

Do not reference non-public source repositories, trackers, wiki names, raw credentials, cookies, internal URLs, or unsanitized API responses. Source systems should appear here only as generic concepts or sanitized fixtures.

## Read First

1. `README.md`
2. `docs/architecture.md`
3. `docs/cache-and-sync-model.md`
4. `docs/gitcode-api-discovery.md`
5. `docs/mcp-setup.md`
6. the linked GitCode issue or pull request for the current task

## Engineering Defaults

- Keep reads cache-first. Routine agent/coordinator reads must not require live network access.
- Treat GitCode live API behavior as an adapter detail behind captured compatibility evidence.
- Preserve stable source ids such as `DOC-123`; remote issue ids are aliases, not replacements.
- Prefer deterministic exports and sync logs so changes can be reviewed.
- Gate writes behind explicit commands and idempotency keys.
- Keep credentials out of repository files, logs, fixtures, and test snapshots.

## Code Layout

- `cmd/gitcode-mcp/`: CLI entrypoint.
- `internal/`: package code and unit tests.
- `docs/`: durable technical docs.
- `testdata/`: sanitized fixtures.
- `tests/`: higher-level validation scenarios.

## Before Committing

Run:

```sh
go test ./...
git diff --check
```

If tests cannot run because the task depends on unavailable network/API credentials, record that explicitly on the relevant issue or pull request and keep the test fixture boundary clear.
