# Validation Scenarios: 020 Add config startup API

## 020-operations-security-task-1-add-config-startup-api-scenario-1

Actor trigger: a developer runs startup config tests. The validation executes the repository's offline Go test path for startup configuration behavior.

## 020-operations-security-task-1-add-config-startup-api-scenario-2

Target product surface/API: `internal/config.Load`, `internal/config.Token`, `internal/config.RedactDiagnostic`, and CLI override inputs consumed by `cmd/gitcode-mcp`. The validation runs product package tests under `internal/...` that directly exercise the public config API and override merge surface.

## 020-operations-security-task-1-add-config-startup-api-scenario-3

Expected outcome: a temporary config selected by `GITCODE_CONFIG` overrides `cache_path`; absent default config uses defaults; explicit missing/malformed `$GITCODE_CONFIG` returns a redacted error; malformed present default config returns a redacted error; `GITCODE_TOKEN` is available through `Token` but absent from serialized/logged `Config`; a `--timeout 10s` override wins over configured `30s`.

Concrete checks are provided by `TestConfigLoading` and `TestCLIFlagOverride` in the production Go test suite, including deterministic memory-backed config sources and no live network access.

## 020-operations-security-task-1-add-config-startup-api-scenario-4

Executable evidence: `go test ./internal/... -run TestConfigLoading` and `go test ./internal/... -run TestCLIFlagOverride` pass.

The validation script also runs `go test ./...` and `git diff --check` to catch compile/import regressions and whitespace errors without modifying production source.
