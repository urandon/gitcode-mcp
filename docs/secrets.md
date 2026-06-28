# Secrets

## Token storage policy

The GitCode API token is provided via the `GITCODE_TOKEN` environment variable or the configured system keyring. It is never stored in config files, logs, fixtures, snapshots, or any repository-tracked file.

## Platform-specific credential storage

### macOS Keychain

Save the token:

```sh
security add-generic-password \
  -a token \
  -s "gitcode-mcp" \
  -w "<your-token>" \
  -U
```

The runtime reads service `gitcode-mcp`, account `token` through the system keyring. `credential.store: keychain` remains accepted as a legacy alias for `credential.store: keyring`.

For multiple local agents or configs, use a distinct keyring account per profile:

```yaml
credential:
  store: keyring
  keyring_service: gitcode-mcp
  keyring_account: codex-write
```

Then save the token under the same account:

```sh
security add-generic-password \
  -a codex-write \
  -s "gitcode-mcp" \
  -w "<your-token>" \
  -U
```

Wrapper script to launch with Keychain token when you prefer exporting `GITCODE_TOKEN` only for the child process:

```zsh
#!/usr/bin/env zsh
set -euo pipefail

GITCODE_MCP_BIN="${GITCODE_MCP_BIN:-gitcode-mcp}"
KEYCHAIN_SERVICE="${GITCODE_MCP_KEYCHAIN_SERVICE:-gitcode-mcp}"
KEYCHAIN_ACCOUNT="${GITCODE_MCP_KEYCHAIN_ACCOUNT:-token}"

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

Using Secret Service directly:

```sh
secret-tool store --label='gitcode-mcp token' service gitcode-mcp username token
```

The runtime reads that entry through the system keyring when `credential.store` is `auto` or `keyring`.

For an agent-specific token, match the configured account:

```sh
secret-tool store --label='gitcode-mcp codex-write' service gitcode-mcp username codex-write
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
cmdkey /generic:gitcode-mcp:token /user:token /pass:<your-token>
```

The runtime reads the Credential Manager target used by the Go keyring library when `credential.store` is `auto` or `keyring`.

For an agent-specific token, match the configured account:

```powershell
cmdkey /generic:gitcode-mcp:codex-write /user:codex-write /pass:<your-token>
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
- Live network access requires `GITCODE_TOKEN` or a configured system keyring token and explicit command intent.
