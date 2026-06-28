# Config Reference

## Configuration sources

Configuration is loaded from five layers, in order of precedence:

1. Command-line overrides (`--cache-path`, `--timeout`, `--max-size`, `--format`)
2. Cache environment overrides such as `GITCODE_MCP_CACHE_DIR`
3. YAML config file (at `GITCODE_MCP_CONFIG` path or default location)
4. Repo-local config opt-in from `.gitcode/gitcode-mcp.yaml`
5. Built-in defaults

Legacy JSON config files are still recognized through `GITCODE_CONFIG`.

## Default config location

| Platform | Path |
|---|---|
| macOS | `$HOME/Library/Application Support/gitcode-mcp/config.yaml` |
| Linux | `$XDG_CONFIG_HOME/gitcode-mcp/config.yaml` (falls back to `$HOME/.config/gitcode-mcp/config.yaml`) |
| Windows | `%AppData%/gitcode-mcp/config.yaml` |

Override with the `GITCODE_MCP_CONFIG` environment variable:

```sh
export GITCODE_MCP_CONFIG=/path/to/custom/config.yaml
```

## Config file format (YAML)

```yaml
cache_path: /path/to/cache/gitcode-mcp/cache.db
lock_path: /path/to/cache/gitcode-mcp/cache.db.lock
cache_mode: global
gitcode_base_url: https://api.gitcode.com/api/v5
default_timeout: 30s
max_response_size: 10485760
max_retries: 2
format: text
credential:
  store: auto
  keyring_service: gitcode-mcp
  keyring_account: token
```

### Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `cache_path` | string | `<cache-dir>/gitcode-mcp/cache.db` | SQLite cache database path |
| `lock_path` | string | `<cache_path>.lock` | Lock file path for writer ownership |
| `cache_mode` | string | `global` | Cache placement mode. Use `repo-local` to resolve the cache under the current Git worktree when no explicit cache path is set |
| `gitcode_base_url` | string | `https://api.gitcode.com/api/v5` | GitCode API base URL |
| `default_timeout` | duration | `30s` | Timeout for GitCode API calls and the CLI operation context |
| `max_response_size` | int64 | `10485760` | Maximum response size in bytes |
| `max_retries` | int | `2` | Maximum retries for API calls |
| `format` | string | `text` | Default output format (`text` or `json`) |
| `credential.store` | string | `auto` | Credential lookup mode: `auto` checks `GITCODE_TOKEN` then the system keyring, `env` checks only `GITCODE_TOKEN`, and `keyring` checks the system keyring after env fallback. `keychain` is accepted as a legacy alias for `keyring`. |
| `credential.keyring_service` | string | `gitcode-mcp` | System keyring service name used when `credential.store` is `auto` or `keyring`. Override it to isolate credentials for different agents or profiles. |
| `credential.keyring_account` | string | `token` | System keyring account/user name used when `credential.store` is `auto` or `keyring`. Override it to isolate credentials for different agents or profiles. |

Environment overrides:

| Environment variable | Effect |
|---|---|
| `GITCODE_MCP_KEYRING_SERVICE` | Overrides `credential.keyring_service` for the launched process |
| `GITCODE_MCP_KEYRING_ACCOUNT` | Overrides `credential.keyring_account` for the launched process |

## Repo-local cache mode

Repo-local mode keeps a repository-specific MCP cache under the current Git worktree:

```text
<git-worktree>/
  .gitcode/
    gitcode-mcp.yaml
    mcp/
      cache.db
      cache.db.lock
```

Enable it by committing or creating `.gitcode/gitcode-mcp.yaml` in the worktree:

```yaml
cache_mode: repo-local
```

The cache database and lock are local state. Ignore them with:

```gitignore
.gitcode/mcp/
```

When a command starts inside a worktree, `gitcode-mcp` walks up to the Git root and reads `.gitcode/gitcode-mcp.yaml`. Repo-local cache selection is opt-in and only applies when no command-line cache path, `GITCODE_MCP_CACHE_DIR`, or global config `cache_path` has already selected a cache. This keeps existing global-cache installs compatible while allowing Codex, Zed, and other repo-launched MCP clients to share the same per-repository cache without passing `--cache-path`.

`cache_mode: repo-local` is also accepted in the user config file. In that case the current Git worktree still supplies the repo root, and the cache resolves to `<git-worktree>/.gitcode/mcp/cache.db`.

## Inspecting configuration

### Show config location

```sh
gitcode-mcp config locate
```

Expected: prints the active config file path.

### Show config (redacted)

```sh
gitcode-mcp config show --redacted
```

Expected: prints effective configuration with token value replaced by `[REDACTED]`.
The output includes `cache_path_source`, `cache_mode`, credential keyring service/account, and, when repo-local discovery applies, `repo_root` and `repo_local_config_path`.

### Initialize config

```sh
gitcode-mcp config init
```

Creates the default config file if it does not already exist.

## Secrets

The GitCode API token is provided via the `GITCODE_TOKEN` environment variable or the configured system keyring. It is never stored in config files, logs, fixtures, or snapshots.

```sh
export GITCODE_TOKEN=<your-token>
```

See [Secrets](secrets.md) for platform-specific credential storage patterns.

## Runtime audit

```sh
gitcode-mcp doctor --runtime-audit
```

Emits a JSON report with version, config path, config source, cache path/source/mode, repo-local discovery metadata, credential status, token presence, failure classes, and remediation hints.
