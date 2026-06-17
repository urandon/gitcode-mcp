# Agent Guide

This repository is the implementation home for cache-first GitCode MCP/CLI tooling.

Mission: make GitCode workable for agents and humans under poor network availability by keeping a durable local cache, deterministic exports, link/id resolution, and MCP access over cached data.

## Boundary

Keep this repository self-contained and public-safe.

Use this repo for:

- implementation tasks and backlog;
- code, tests, fixtures, cache schema, CLI, and MCP server work;
- GitCode API experiments and captured fixtures;
- development handoffs for this tooling.

Do not reference non-public source repositories, trackers, wiki names, raw credentials, cookies, internal URLs, or unsanitized API responses. Source systems should appear here only as generic concepts or sanitized fixtures.

## Read First

1. `README.md`
2. `project/tasks/backlog.md`
3. `docs/architecture.md`
4. `docs/cache-and-sync-model.md`
5. `docs/gitcode-api-discovery.md`
6. the specific task or handoff under `project/`

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
- `project/tasks/`: repo-local implementation tasks.
- `project/research/`: API discovery and product/tooling research.
- `project/decisions/`: lightweight ADR-style decisions.
- `project/handoffs/`: implementation handoffs between sessions.

## Before Committing

Run:

```sh
go test ./...
git diff --check
```

If tests cannot run because the task depends on unavailable network/API credentials, record that explicitly in the handoff and keep the test fixture boundary clear.
