# Scenario 011: Auth status reporting and invalid-token diagnostics

All validation exercises below are offline/mock deterministic Go tests with no network access, no real OS keychain, and no live GitCode API calls. The only external dependencies mocked are the OS environment (memSource) and the auth probe callback. The production `internal/credential` package code paths are exercised directly.

## 011-internal-credential-task-2-add-auth-status-reporting-and-invalid-token-diagno-scenario-1

auth status with token → reports source (env/keychain) + redacted value. auth status without token → lists available token source options.

- **Given** a credential pipeline with `GITCODE_TOKEN=glpat-abc123xyz` set
- **When** the credential `Pipeline.Status(ctx)` is executed and rendered via `AuthStatus.RenderText()`
- **Then** the output includes `redacted_token:` with a value matching `glp***xyz` (prefix visible, middle redacted, suffix visible) and never contains the full token
- **And** `token_valid: true` is present in the output
- **When** no `GITCODE_TOKEN` is set
- **Then** the output includes `token_present: false`, lists available sources including `env:GITCODE_TOKEN`, `keychain`, `none`, and has no `redacted_token:` line
- **Product gap**: The CLI's `auth status` command at `internal/cli/cli.go:415` calls `config.DefaultCredentialProvider()` (the old credential path through `config.ChainCredentialProvider`), not the new `credential.Pipeline`. The old `config.CredentialStatus` struct lacks `RedactedToken`, `TokenDiagnostic`, and `AuthProbeResult` fields. The new credential pipeline's `RedactToken()`, `ValidateTokenFormat()`, and `AuthStatus.RenderText()` are implemented and unit-tested in `internal/credential/`, but the CLI integration still routes through `config.RenderCredentialStatus()` which emits no redacted token line. The unit-level credential package behavior satisfies the redaction and validation requirements; the CLI-level output does not.

## 011-internal-credential-task-2-add-auth-status-reporting-and-invalid-token-diagno-scenario-2

GITCODE_TOKEN=invalid sync --live → clear auth-failure diagnostic, not generic HTTP error.

- **Given** a credential pipeline with `GITCODE_TOKEN=invalid_token_service` (a token that will fail auth)
- **When** a probe callback attached via `Pipeline.WithProbe()` returns `false, "expired token"`
- **Then** `Pipeline.Status(ctx)` returns `FailureClass: "auth-failure"` with `Remediation: "expired token"` rather than a generic HTTP error
- **And** `ClassifyAuthError()` recognizes errors implementing `AuthFailure` interface as auth-failure errors, distinguishing them from generic network errors
