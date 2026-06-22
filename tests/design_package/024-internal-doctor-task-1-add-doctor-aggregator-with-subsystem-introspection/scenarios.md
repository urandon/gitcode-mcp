# Validation scenarios: internal doctor task 1

## 024-internal-doctor-task-1-add-doctor-aggregator-with-subsystem-introspection-scenario-1

**Product surface:** `gitcode-mcp doctor --format json` through `go run ./cmd/gitcode-mcp`.

**Given** a temporary offline cache with a sanitized repository binding and `GITCODE_TOKEN` set to a sentinel secret value.

**When** the operator runs the real doctor CLI command against that cache.

**Then** the report includes all readiness dimensions: version, config, cache, repo, credential/token, live provider, auth probe, sync, index, and MCP transport. The cache schema version is surfaced, repo binding is ready, credential status reports a configured token without printing the raw token, sync and index statuses are available, and MCP stdio/HTTP transport readiness is reported without starting a persistent server.

## 024-internal-doctor-task-1-add-doctor-aggregator-with-subsystem-introspection-scenario-2

**Product surface:** `gitcode-mcp doctor --format json` through `go run ./cmd/gitcode-mcp`.

**Given** a fresh temporary offline cache with no repository binding.

**When** the operator runs the real doctor CLI command against that cache.

**Then** the report includes repo status equivalent to `no repo bound`, includes a concrete bind suggestion using `repo add`, and still renders the other readiness dimensions in public-safe form.

## 024-internal-doctor-task-1-add-doctor-aggregator-with-subsystem-introspection-scenario-3

**Product surface:** `gitcode-mcp doctor --format json` through `go run ./cmd/gitcode-mcp`.

**Given** a temporary offline environment with no `GITCODE_TOKEN` and no live/provider access.

**When** the operator runs the real doctor CLI command.

**Then** the report includes `no token configured`, lists available credential sources, and does not expose secrets, Authorization headers, cookies, or private provider payloads.
