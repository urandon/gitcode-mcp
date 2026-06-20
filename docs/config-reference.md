# Config Reference

## Configuration sources

Configuration is loaded from three layers, in order of precedence:

1. Command-line overrides (`--cache-path`, `--timeout`, `--max-size`, `--format`)
2. YAML config file (at `GITCODE_MCP_CONFIG` path or default location)
3. Built-in defaults

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
gitcode_base_url: https://api.gitcode.com/api/v5
default_timeout: 30s
max_response_size: 10485760
max_retries: 2
format: text
credential:
  store: env
```

### Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `cache_path` | string | `<cache-dir>/gitcode-mcp/cache.db` | SQLite cache database path |
| `lock_path` | string | `<cache_path>.lock` | Lock file path for writer ownership |
| `gitcode_base_url` | string | `https://api.gitcode.com/api/v5` | GitCode API base URL |
| `default_timeout` | duration | `30s` | Timeout for GitCode API calls |
| `max_response_size` | int64 | `10485760` | Maximum response size in bytes |
| `max_retries` | int | `2` | Maximum retries for API calls |
| `format` | string | `text` | Default output format (`text` or `json`) |

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

### Initialize config

```sh
gitcode-mcp config init
```

Creates the default config file if it does not already exist.

## Secrets

The GitCode API token is provided via the `GITCODE_TOKEN` environment variable. It is never stored in config files, logs, fixtures, or snapshots.

```sh
export GITCODE_TOKEN=<your-token>
```

See [Secrets](secrets.md) for platform-specific credential storage patterns.

## Runtime audit

```sh
gitcode-mcp doctor --runtime-audit
```

Emits a JSON report with version, config path, config source, cache path, credential status, token presence, failure classes, and remediation hints.
