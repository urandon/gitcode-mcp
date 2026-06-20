# Troubleshooting

## Common issues and diagnostics

### Command not found

```text
gitcode-mcp: command not found
```

**Cause:** Binary is not in PATH.

**Fix:** Build and install the binary. See [Install](install.md).

### Config file not found

```text
config: cannot read config file
```

**Cause:** Config file does not exist at default location and `GITCODE_MCP_CONFIG` is not set.

**Diagnostic:**
```sh
gitcode-mcp config locate
```

**Fix:**
```sh
gitcode-mcp config init
```

Or create the file manually at the default path.

### No token present

```text
gitcode-mcp auth status
```

Reports token absent.

**Cause:** `GITCODE_TOKEN` environment variable is not set.

**Fix:** Set the token. See [Secrets](secrets.md).

### Cache database locked

```text
cache: database is locked
```

**Cause:** Another process holds the writer lock (sync or index in progress).

**Diagnostic:**
```sh
gitcode-mcp cache-status --repo example-owner/example-repo
```

**Fix:** Wait for the writer to complete, or terminate the process holding the lock.

### Cache database unreadable

```text
cache_unreadable
```

**Cause:** Cache database cannot be opened (permissions, corruption, wrong path).

**Diagnostic:**
```sh
gitcode-mcp doctor --runtime-audit
```

**Fix:** Check file permissions on the cache path. Run `doctor --runtime-audit` for failure classes.

### No repositories configured

```text
repo_unavailable
```

**Cause:** No repositories have been added to the cache.

**Fix:**
```sh
gitcode-mcp repo add --owner <owner> --repo <name> --scopes issues,wiki
```

### Sync has not been run

```text
no cached records for repo
```

**Cause:** Cache is empty for the specified repository.

**Fix:**
```sh
gitcode-mcp sync --repo example-owner/example-repo --input fixtures/api/v5/repos/example-owner/example-repo
```

### Index is stale or missing

```text
index_stale
```

**Cause:** Cache has records but the index is out of date or missing.

**Diagnostic:**
```sh
gitcode-mcp stale-index --repo example-owner/example-repo
```

**Fix:**
```sh
gitcode-mcp index --repo example-owner/example-repo
```

### GitCode API unavailable

```text
adapter_unavailable
```

**Cause:** Network is unavailable, token is missing, or API endpoint is unreachable.

**Diagnostic:** Check `GITCODE_TOKEN` and network connectivity. The `doctor --runtime-audit` command reports credential status and failure classes.

**Fix:** Reads work offline from cache. Writes require network and token.

### Alias collision

```text
ambiguous alias: multiple repositories match
```

**Cause:** Two repositories share the same alias.

**Fix:** Use the full `repo_id` (`--repo owner/name`) instead of the alias.

### Write rejected (dry-run without --live)

```text
write: --live flag required for mutation
```

**Cause:** Write command was run without `--live`.

**Fix:** Add `--dry-run` to validate, or `--live` to execute (requires `GITCODE_TOKEN` and network).

## Runtime audit

```sh
gitcode-mcp doctor --runtime-audit
```

Emits a JSON report covering:

- `version` — gitcode-mcp version
- `config_path` — active config file path
- `config_source` — how config was located (`env`, `defaults`, `file`)
- `config_exists` — whether config file exists
- `cache_path` — resolved cache path
- `credential_source` — how token was resolved
- `token_present` — whether token is available
- `failure_classes` — list of detected failure classes
- `remediation` — suggested remediation steps

### Failure classes

| Class | Meaning |
|---|---|
| `no-config` | Config file does not exist |
| `config-unreadable` | Config file exists but cannot be read |
| `config-malformed` | Config file contains invalid JSON or bad values |
| `legacy-config` | Config uses an older format (YAML) |
| `no-token` | No GitCode API token available |
| `keychain-unavailable` | OS credential store unavailable |

All paths and token values are redacted in the output.

## Getting more diagnostic output

Use `--format json` for structured error output:

```sh
gitcode-mcp get --repo example-owner/example-repo --id NONEXISTENT --format json
```

Expected: JSON error response with error class and message.
