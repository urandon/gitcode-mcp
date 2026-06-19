# Validation Scenarios: ConfigCommands add redacted UX

## 001-config-credential-task-1-configcommands-add-redacted-ux-scenario-1

A developer runs `gitcode-mcp config init`, `gitcode-mcp config locate`, `gitcode-mcp config show --redacted`, and `gitcode-mcp auth status` against a temporary config home with mocked OS credential providers; the CLI displays the active config path/source, canonical format, legacy JSON compatibility source when applicable, field override sources, cache path resolution, credential source, token presence without value, and platform/headless remediation.

Concrete offline checks:

- Build the real `cmd/gitcode-mcp` CLI binary.
- Use an isolated temporary `HOME`, `XDG_CONFIG_HOME`, and `XDG_CACHE_HOME`.
- Run `config init` with `GITCODE_MCP_CONFIG` set to a temporary YAML path.
- Assert the command succeeds, reports YAML, writes only the YAML path, and does not create a sibling JSON file.
- Run `config locate` and assert it reports the same active path, `explicit-yaml`, `yaml`, and `config_exists: true`.
- Run `config show --redacted` with file values plus env overrides and `GITCODE_TOKEN`; assert cache/API env sources win, credential source is env, token presence is true, and no secret value appears.
- Run `auth status` with no token and `credential.store: env`; assert missing-token remediation is visible without provider diagnostics.
- Run `config locate` with legacy `GITCODE_CONFIG` JSON and no default YAML; assert `legacy-json` and `json` are reported.

## 001-config-credential-task-1-configcommands-add-redacted-ux-scenario-2

Local CLI tests exercise the real command routes with `GITCODE_MCP_CONFIG`, default YAML path, legacy `GITCODE_CONFIG` JSON, env overrides, env-only mode, keyring-present, keyring-unavailable, and missing-token states; they verify exit codes and visible output, assert env overrides win duplicate fields, assert `config init` writes YAML only, and assert no token, config-file secret, or raw credential-store diagnostic appears in stdout/stderr while sanitized temporary paths are allowed and expected.

Concrete offline checks:

- Execute the repository Go test suite for the config and CLI packages through `go test ./internal/config ./internal/cli` so mock credential provider scenarios exercise the real command dispatch code.
- Execute the real built CLI against temporary homes for explicit YAML, default YAML, legacy JSON, env override, env-only, and missing-token scenarios.
- Enforce negative leak checks against known token strings, file-contained secret strings, and raw credential-store diagnostic text in all captured stdout/stderr.
- Fail non-zero on any missing output field, wrong exit code, JSON creation by `config init`, env precedence regression, or secret/diagnostic leakage.
