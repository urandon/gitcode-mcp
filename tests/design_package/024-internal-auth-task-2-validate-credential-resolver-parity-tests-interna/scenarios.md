# Validation Scenarios

Task: 024-internal-auth-task-2-validate-credential-resolver-parity-tests-interna

## Scenario Inventory

- `024-internal-auth-task-2-validate-credential-resolver-parity-tests-interna-scenario-1`: go test ./internal/auth/... passes.
- `024-internal-auth-task-2-validate-credential-resolver-parity-tests-interna-scenario-2`: All credential resolution scenarios verified.
- `024-internal-auth-task-2-validate-credential-resolver-parity-tests-interna-scenario-3`: Resolver invoked once per command; result deterministically passed to all paths.
- `024-internal-auth-task-2-validate-credential-resolver-parity-tests-interna-scenario-4`: Priority order env var > basic auth > keychain confirmed by test assertions..

## Product Scenarios

### SCN-024-01: go test ./internal/auth/... passes with all 6 credential resolver tests

**Product behavior:** Running `go test -count=1 ./internal/auth/...` exits zero and all 6 test functions pass, confirming the credential resolver implementation is correct and complete.

**Test files:** `internal/auth/resolver_test.go`

**Verification:**
- `go test -count=1 ./internal/auth/...` exits 0
- All 6 test names appear in the output as PASS:
  - `TestCredentialResolverEnvTokenPresent`
  - `TestCredentialResolverMockKeychain`
  - `TestCredentialResolverNoCredential`
  - `TestCredentialResolverDeterministic`
  - `TestCredentialResolverStatusMatchesResolve`
  - `TestCredentialResolverEnvOverKeychain`

---

### SCN-024-02: All credential resolution scenarios verified

**Product behavior:** All credential-parity scenarios are exercised by the existing test suite:

- **credential-present-env:** GITCODE_TOKEN env var set → `Present=true`, `Source="env:GITCODE_TOKEN"`, correct token value (`TestCredentialResolverEnvTokenPresent`)
- **credential-mock-keychain:** GITCODE_TOKEN not set but mock keychain token available → `Present=true`, `Source="mock-keychain"` (`TestCredentialResolverMockKeychain`)
- **no-credential:** No credential at all → `Present=false`, `ErrorClass="token-missing"`, non-empty remediation mentioning GITCODE_TOKEN (`TestCredentialResolverNoCredential`)
- **deterministic-resolution:** `Resolve()` called multiple times returns the same `Result` object — resolver invoked once internally, result idempotent (`TestCredentialResolverDeterministic`)
- **status-matches-resolve:** `Status()` returns the same result as `Resolve()` — auth status and write paths share the same resolution pipeline (`TestCredentialResolverStatusMatchesResolve`)
- **priority-env-over-keychain:** Both env token and keychain available → env takes priority (`TestCredentialResolverEnvOverKeychain`)

**Test files:** `internal/auth/resolver_test.go`

**Verification:**
- `go test -count=1 -run 'TestCredential' -v ./internal/auth/...` exits 0 and shows PASS for all 6 tests

---

### SCN-024-03: Resolver invoked once per command; result deterministically passed to all paths

**Product behavior:** `CredentialResolver.Resolve()` memoizes the result after the first call. Subsequent calls to both `Resolve()` and `Status()` return the same deterministic `Result` struct. This ensures that `auth status`, read probes, and write commands see identical credential state from a single resolution.

**Implementation detail:**
- `resolver.go:38-53`: `Resolve()` checks `r.result != nil` and returns the cached result. On first call, it invokes `r.provider.Resolve()` once and stores the `Result`.
- `resolver.go:55-57`: `Status()` delegates to `Resolve()`, guaranteeing parity.

**Test files:** `internal/auth/resolver_test.go`

**Verification:**
- `TestCredentialResolverDeterministic` proves Resolve() returns identical result structs across calls
- `TestCredentialResolverStatusMatchesResolve` proves Status() == Resolve() output

---

### SCN-024-04: Priority order env var > basic auth > keychain confirmed by test assertions

**Product behavior:** The priority order is `env:GITCODE_TOKEN` > `mock-keychain` (development) / `keychain` (production). By run plan constraint, basic auth (`GITCODE_USER`/`GITCODE_PASS`) is excluded from the current scope.

**Implementation detail:**
- `config/effective.go:179-188`: `DefaultCredentialProvider` chains `EnvCredentialProvider` before `KeychainCredentialProvider` (with mock-keychain shortcut via `GITCODE_MCP_TEST_KEYCHAIN_TOKEN`)
- `config/effective.go:203-231`: `ChainCredentialProvider.ResolveLiveCredential` returns on the first provider that yields a present, non-empty token — env provider is checked first

**Test files:** `internal/auth/resolver_test.go`

**Verification:**
- `TestCredentialResolverEnvTokenPresent` proves env:GITCODE_TOKEN → credential present
- `TestCredentialResolverEnvOverKeychain` proves env token takes priority when both env and keychain tokens are available
- `TestCredentialResolverMockKeychain` proves mock-keychain fallback when env token absent

Note: basic auth (`GITCODE_USER`/`GITCODE_PASS`) priority position between env token and keychain is not exercised because the run plan constraint explicitly excludes basic auth. The acceptance criteria phrase "env var > basic auth > keychain" describes the target architecture, but the implementation scope is env token > keychain only.
