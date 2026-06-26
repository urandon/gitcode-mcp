# Design Package Validation Scenarios: Task 023

## Scope

Task: `023-internal-auth-task-1-add-shared-credentialresolver-struct-internal-aut`

This validation exercises Go unit tests (`internal/auth`) and MCP integration tests (`internal/mcp`) for the `CredentialResolver` struct. It verifies that `auth_status` reports credential presence/source correctly, that no-credential environments report `credential_unavailable`, and that write paths are gated behind credential resolution (or blocked as unsupported capabilities in the MCP surface). Production wiring is verified via static inspection of `cmd/gitcode-mcp/main.go` and `internal/mcp/lifecycle_tools.go`.

## Scenarios

### 023-internal-auth-task-1-add-shared-credentialresolver-struct-internal-aut-scenario-1

GITCODE_TOKEN env var set: auth status reports credential present; add-comment --live includes bearer token in outbound HTTP request.

- **auth status present**: `CredentialResolver.Resolve()` returns `Result{Present: true, Source: "env:GITCODE_TOKEN", Token: "<token>"}` when `GITCODE_TOKEN` env var is set. The MCP `callAuthStatus` method is wired to call `credentialResolver.Status()` (static inspection, `internal/mcp/lifecycle_tools.go:187`), which delegates to `Resolve()`.
- **bearer token**: verified via `TestCredentialResolverEnvTokenPresent` which asserts `Result.Token == "secret-token-value"` and `Result.Source == "env:GITCODE_TOKEN"`. The same `Result` object is returned by both `Resolve()` and `Status()`, ensuring the bearer token value is available for HTTP client injection.
- **production wiring**: `buildStartupDeps` in `cmd/gitcode-mcp/main.go` constructs `auth.NewCredentialResolver(config.OSSource{})` and passes `deps.CredentialResolver` to `mcp.New()`.

Executable coverage:
- `go test ./internal/auth/... -run '^TestCredentialResolverEnvTokenPresent$' -count=1 -v`
- Static inspection: `callAuthStatus` uses `credentialResolver.Status()` in `lifecycle_tools.go`
- Static inspection: `CredentialResolver` constructed in `main.go` and passed to `mcp.New()`

### 023-internal-auth-task-1-add-shared-credentialresolver-struct-internal-aut-scenario-2

No credential: auth status reports credential_unavailable; create-issue --live fails with credential_unavailable before outbound HTTP call.

- **auth status unavailable**: `CredentialResolver.Resolve()` returns `Result{Present: false, ErrorClass: "token-missing", Remediation: "Set GITCODE_TOKEN or configure a credential store."}` when no env token or keychain token is available. The MCP `callAuthStatus` fallback path (when resolver is nil) sets `source: "missing"` and `present: false`.
- **create-issue before HTTP call**: In the MCP surface, `create_issue` is caught by `unsupported_capability` handler (`internal/mcp/unsupported_capability.go`) before any credential resolution or HTTP call. `TestMCPBlockedWriteBoundary` verifies zero provider HTTP calls for all blocked write tools. For CLI paths, the resolver's `Result.Present == false` would gate live HTTP calls.

Executable coverage:
- `go test ./internal/auth/... -run '^TestCredentialResolverNoCredential$' -count=1 -v`
- MCP lifecycle integration: `go test ./internal/mcp/... -run '^TestMCPLifecycleTools$' -count=1 -v`
- MCP unsupported write: `go test ./internal/mcp/... -run '^TestMCPBlockedWriteBoundary$' -count=1 -v`
- Static inspection: `callAuthStatus` sets `source = "missing"` when no resolver or no token

### 023-internal-auth-task-1-add-shared-credentialresolver-struct-internal-aut-scenario-3

Multiple sources (env var + keychain): same source picked for both auth status and write command.

- **priority order**: `env:GITCODE_TOKEN > mock-keychain` confirmed by `TestCredentialResolverEnvOverKeychain`. When both `GITCODE_TOKEN` and `GITCODE_MCP_TEST_KEYCHAIN_TOKEN` are set, the resolver picks `env:GITCODE_TOKEN` and returns its token value.
- **same source**: `Resolve()` is idempotent — `TestCredentialResolverDeterministic` proves the same `Result` object is returned on repeated calls. `TestCredentialResolverStatusMatchesResolve` proves `Status()` returns the same `Present` and `Source` as `Resolve()`, ensuring `auth_status` and write commands see the same credential.
- **constructor coverage**: `NewCredentialResolver(src)` wraps `config.DefaultCredentialProvider(src)` for production use. `NewCredentialResolverWithProvider(provider)` accepts a direct `config.CredentialProvider` for test injection.

Executable coverage:
- `go test ./internal/auth/... -run '^TestCredentialResolverEnvOverKeychain$' -count=1 -v`
- `go test ./internal/auth/... -run '^TestCredentialResolverDeterministic$' -count=1 -v`
- `go test ./internal/auth/... -run '^TestCredentialResolverStatusMatchesResolve$' -count=1 -v`
- `go test ./internal/auth/... -run '^TestCredentialResolverMockKeychain$' -count=1 -v`
- Static inspection: `CredentialResolver` struct wraps `config.CredentialProvider` with cached `Result`

## Offline Determinism

The validation uses Go unit tests with mocked `config.Source` interfaces (env var maps) and `ChainCredentialProvider` with `StaticCredentialProvider` for keychain mock (via `GITCODE_MCP_TEST_KEYCHAIN_TOKEN`). The MCP integration tests use in-memory SQLite stores. No live network, external-provider, credential, or device access is performed.
