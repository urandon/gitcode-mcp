# Secrets

## Token storage policy

The GitCode API token is always provided via the `GITCODE_TOKEN` environment variable. It is never stored in config files, logs, fixtures, snapshots, or any repository-tracked file.

## Platform-specific credential storage

### macOS Keychain

Save the token:

```sh
security add-generic-password \
  -a "$USER" \
  -s "gitcode-mcp" \
  -w "<your-token>" \
  -U
```

Wrapper script to launch with Keychain token:

```zsh
#!/usr/bin/env zsh
set -euo pipefail

GITCODE_MCP_BIN="${GITCODE_MCP_BIN:-gitcode-mcp}"
KEYCHAIN_SERVICE="${GITCODE_MCP_KEYCHAIN_SERVICE:-gitcode-mcp}"
KEYCHAIN_ACCOUNT="${GITCODE_MCP_KEYCHAIN_ACCOUNT:-${USER}}"

token="$(security find-generic-password \
  -a "${KEYCHAIN_ACCOUNT}" \
  -s "${KEYCHAIN_SERVICE}" \
  -w 2>/dev/null || true)"

if [[ -z "${token}" ]]; then
  print -u2 "gitcode-mcp: no token in Keychain service '${KEYCHAIN_SERVICE}' account '${KEYCHAIN_ACCOUNT}'"
  print -u2 "Save with: security add-generic-password -a \"${KEYCHAIN_ACCOUNT}\" -s \"${KEYCHAIN_SERVICE}\" -w \"<token>\" -U"
  exit 1
fi

export GITCODE_TOKEN="${token}"
unset token
exec "${GITCODE_MCP_BIN}" "$@"
```

### Linux (D-Bus Secret Service / pass)

Using `secret-tool`:

```sh
secret-tool store --label='gitcode-mcp' service gitcode-mcp account "$USER"
```

Using `pass`:

```sh
pass insert gitcode-mcp/token
```

Wrapper script:

```sh
#!/usr/bin/env bash
set -euo pipefail
export GITCODE_TOKEN="$(pass gitcode-mcp/token)"
exec gitcode-mcp "$@"
```

### CI / headless environments

Set the environment variable directly:

```sh
export GITCODE_TOKEN="<ci-token>"
gitcode-mcp sync --repo example-owner/example-repo
```

Ensure the token is stored in the CI secret management system (not committed to version control).

### Windows Credential Manager

```powershell
cmdkey /generic:gitcode-mcp /user:%USERNAME% /pass:<your-token>
```

## Verifying token status

```sh
gitcode-mcp auth status
```

Expected output indicates whether a token is present (without printing the token value).

## Redaction policy

All diagnostics, logs, and error messages redact token values. The config show command displays token presence as `[REDACTED]`:

```sh
gitcode-mcp config show --redacted
```

## Public-safety invariant

- Tokens are never committed to the repository.
- Fixtures and test data contain no real tokens.
- Config files contain only non-secret paths and API base URLs.
- Live network access requires explicit `GITCODE_TOKEN` and command flags.
