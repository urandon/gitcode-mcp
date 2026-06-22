# gitcode-mcp

Cache-first MCP and CLI tooling for working with GitCode when network availability is poor.

The mission is to make GitCode usable for agents and humans even when live access is slow, flaky, or unavailable. The project should provide local cache, search, link resolution, deterministic exports, and eventually MCP reads/writes around GitCode tracker/wiki data.

This repository is a standalone, public-safe tooling project. It owns implementation issues, code, tests, fixtures, CLI/MCP implementation, development handoffs, and repository-local engineering notes.

Source repositories, trackers, and wikis are external inputs. Keep examples sanitized and avoid committing non-public source names, credentials, cookies, internal URLs, or raw API responses; see [Sanitization Rules](docs/sanitization.md) for the full public-safety contract.

## Current Shape

```text
.
├── AGENTS.md              # agent operating guide
├── docs/                  # architecture, API discovery, cache/sync docs
├── project/               # lightweight project management
│   ├── decisions/
│   ├── handoffs/
│   ├── research/
│   └── tasks/
├── scripts/               # local helper scripts
├── cmd/gitcode-mcp/       # CLI entrypoint
└── internal/              # Go packages
```

## First Goals

1. Ingest markdown, tracker, or wiki exports into a local cache.
2. Resolve stable source ids such as `DOC-123` to local records and, later, remote issue/wiki ids.
3. Provide cache-first CLI commands for search, get, backlinks, link-check, export, diff, and sync status.
4. Add a read-first MCP server only after the cache contract is clear.
5. Keep live network writes explicit, idempotent, and logged.

## Quick Start

```sh
go test ./...
go run ./cmd/gitcode-mcp --help
go run ./cmd/gitcode-mcp search DOC-123
```

The current CLI is a scaffold. It defines the intended command surface, but the cache, GitCode adapter, and MCP server still need implementation.
