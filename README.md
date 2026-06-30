# gitcode-mcp

Cache-first GitCode tooling for agents and humans.

`gitcode-mcp` keeps GitCode issues, pull requests, wiki pages, comments, and links available through a durable local cache. It exposes that cache through a CLI and an MCP server so agents can search, read, plan, and perform explicit live writes even when network access is slow or unreliable.

The project is self-contained and public-safe. Source repositories, trackers, and wikis are external inputs; examples use placeholders and sanitized fixtures. See [Sanitization Rules](docs/sanitization.md) for the full safety contract.

## What It Does

- Binds GitCode repositories to local cache identities and aliases.
- Syncs issues, pull requests, comments, wiki pages, labels, and milestones into SQLite.
- Searches cached records with full-text/token matching and reads cached records without requiring live network access.
- Resolves stable local ids and remote aliases for links, snippets, backlinks, and exports.
- Runs an MCP server over cached data for agent workflows.
- Performs live writes only through explicit commands with idempotency keys and audit evidence.
- Supports issue, wiki, comment, and pull request workflows from the same cache-first service layer.

## Quick Start

```sh
go test ./...
go run ./cmd/gitcode-mcp --help
go run ./cmd/gitcode-mcp repo add --repo YOUR_OWNER/YOUR_REPO --owner YOUR_OWNER --name YOUR_REPO --scopes issues,wiki
go run ./cmd/gitcode-mcp sync --repo YOUR_OWNER/YOUR_REPO --issues --wiki --pulls --comments
go run ./cmd/gitcode-mcp search --repo YOUR_OWNER/YOUR_REPO "cache-first"
```

`search` is cache full-text search, not fuzzy or semantic retrieval. Empty results mean the exact query terms did not match cached text; retry with exact terms, ids, or keyword variants when wording may differ.

For MCP usage, start with [MCP Setup](docs/mcp-setup.md). For live credentials, start with [Secrets](docs/secrets.md) and [Config Reference](docs/config-reference.md).

## Common Workflows

- Read from cache: [Read Walkthrough](docs/read-walkthrough.md)
- Perform explicit writes: [Write Walkthrough](docs/write-walkthrough.md)
- Work with PR/MR flow: [PR/MR Workflow](docs/pr-mr-workflow.md)
- Install or publish releases: [Install](docs/install.md), [Release Process](docs/release-process.md)
- Review component boundaries: [Component Architecture](docs/component-architecture.md)
- Place tests and fixtures: [Test Architecture](docs/test-architecture.md)
- Configure repositories: [Repository Binding](docs/repo-binding.md)
- Understand sync behavior: [Cache and Sync Model](docs/cache-and-sync-model.md)
- Review live API findings: [GitCode API Discovery](docs/gitcode-api-discovery.md)

## Repository Layout

- `cmd/gitcode-mcp/`: CLI entrypoint.
- `internal/`: cache, service, provider, CLI, MCP, sync, diagnostics, and tests.
- `docs/`: durable product, architecture, operations, and API documentation.
- `testdata/`: sanitized reusable fixture inputs.

Active planning belongs in GitCode issues and pull requests. Historical research or dogfood evidence that is still useful belongs in the GitCode wiki, not in main.
