# MCP Installation And Secrets Notes

This note captures the current dogfood installation pain for `gitcode-mcp` and frames the design work still needed. It is intentionally public-safe: it documents generic paths, environment variables, and wrapper patterns without committing tokens, private hostnames, personal paths, or machine-specific MCP configuration.

## Current Contract

The current startup contract is simple but under-documented:

- `gitcode-mcp --mcp` starts the stdio MCP server.
- The first iteration currently documents stdio as the only startup mode. The second iteration should add a shared HTTP/SSE server mode for multi-client use.
- `GITCODE_CONFIG` points to a JSON config file.
- `GITCODE_TOKEN` carries the GitCode API token and must stay outside config files, logs, fixtures, and snapshots.
- The default config path should be treated as user-local state, usually `${XDG_CONFIG_HOME:-$HOME/.config}/gitcode-mcp/config.json`.
- The default cache path should be treated as user-local state, usually under the OS cache directory.

Example config:

```json
{
  "cache_path": "/path/to/user/cache/gitcode-mcp/cache.db",
  "default_timeout": "30s",
  "max_response_size": 10485760,
  "max_retries": 2,
  "format": "text",
  "gitcode_base_url": "https://api.gitcode.com/api/v5"
}
```

## Mac Keychain Wrapper Pattern

For local dogfood on macOS, a wrapper can keep the token in Keychain and export `GITCODE_TOKEN` only for the child process:

```zsh
#!/usr/bin/env zsh
set -euo pipefail

gitcode_mcp_bin="${GITCODE_MCP_BIN:-gitcode-mcp}"
config_path="${GITCODE_CONFIG:-${HOME}/.config/gitcode-mcp/config.json}"
keychain_service="${GITCODE_MCP_KEYCHAIN_SERVICE:-gitcode-mcp}"
keychain_account="${GITCODE_MCP_KEYCHAIN_ACCOUNT:-${USER}}"

token="$(security find-generic-password -a "${keychain_account}" -s "${keychain_service}" -w 2>/dev/null || true)"
if [[ -z "${token}" ]]; then
  print -u2 "gitcode-mcp: no token found in Keychain service '${keychain_service}' for account '${keychain_account}'"
  print -u2 "Save one with: security add-generic-password -a \"${keychain_account}\" -s \"${keychain_service}\" -w \"<token>\" -U"
  exit 1
fi

export GITCODE_TOKEN="${token}"
export GITCODE_CONFIG="${config_path}"
unset token

exec "${gitcode_mcp_bin}" --mcp "$@"
```

This is useful enough for local operation, but it is not a full product experience. A user still has to know how to install the binary, create the config, store the token, point the MCP client at the wrapper, and verify that the read path works.

## Shared HTTP/SSE Transport Gap

Stdio is a good compatibility mode when one MCP client launches its own server process, but it is awkward for the intended dogfood topology: one cache-first service, one local database, and multiple clients or agents such as Codex, Zed, editor integrations, and workflow components querying the same knowledge layer.

The second iteration should design and implement a shared HTTP/SSE MCP server mode alongside stdio:

- A long-lived `gitcode-mcp` server owns the cache database and serves multiple clients.
- Routine reads should be concurrent and cache-only after bootstrap.
- Sync, index, migrations, and any future write operations should be explicit and serialized through the server, with actionable lock/busy errors.
- The server should expose health/readiness diagnostics so clients can distinguish "server unavailable", "cache empty", "index stale", and "sync running".
- Docs should show both local stdio setup and shared HTTP/SSE setup, including bind address defaults, localhost-only/security defaults, and client configuration.
- The design should verify current MCP transport naming and compatibility, including whether the ecosystem expects legacy HTTP SSE, newer streamable HTTP, or both.

## Config Discoverability Gap

The current dogfood experience makes the active config location too implicit. A user should not need to inspect source code or ask an agent to learn which config file is being read, which file they are expected to edit, and which environment variables are overriding defaults.

The product should make this explicit through docs and command behavior:

- `config init` should create or print the default config path before writing.
- `config locate` or `doctor` should print the active config path, whether it came from `GITCODE_CONFIG` or the default resolver, and whether the file exists.
- `config show --redacted` should print the effective non-secret configuration, redacting any secret-shaped values if they are ever supported.
- Startup errors should say which config path was attempted and how to override it.
- MCP setup docs should show both direct binary invocation and wrapper invocation, including where `GITCODE_CONFIG` is set.

## Design Gaps To Close

- Installation path: document `go install`, local source build, and later release-binary or package-manager flows.
- Config discoverability path: document default config resolution, `GITCODE_CONFIG` precedence, active-config inspection, and which file the user should edit.
- Secret storage path: define recommended patterns for macOS Keychain, Linux Secret Service or `pass`, Windows Credential Manager, and CI/headless environments.
- Product commands: consider whether `gitcode-mcp auth login`, `auth status`, `auth logout`, `config init`, `config locate`, `config show --redacted`, and `doctor` should exist, or whether docs plus wrappers are enough for the first slice.
- MCP client setup: document generic stdio command/wrapper setup and shared HTTP/SSE server setup without assuming one private client configuration.
- Shared-server concurrency: define database ownership, connection settings, locks, busy timeout behavior, index/sync serialization, and how multiple clients observe server/cache status.
- Read path quickstart: after install and secrets, a user should be able to populate cache from a test GitCode repo, run `index`, verify `search/get/snippet/backlinks/sync-status`, and then use the same cache through MCP.
- Failure diagnostics: missing binary, missing config, missing token, bad token, network unavailable, empty cache, stale cache, and unsupported GitCode API routes should produce distinct actionable messages.
- Public-safety invariant: config files may contain non-secret paths and API base URLs, but tokens remain environment/keychain/credential-store only.

## Desired First-Run Experience

The target first-run flow should be explicit and boring:

```sh
gitcode-mcp config init
gitcode-mcp auth status
gitcode-mcp sync --repo example-owner/example-repo --issue 1 --id TASK-0001
gitcode-mcp index
gitcode-mcp search TASK-0001
gitcode-mcp snippet TASK-0001 --line-start 1 --line-end 20
gitcode-mcp --mcp
```

The exact commands may change, but the user experience should not require reading Go source or reconstructing environment variables from tests.
