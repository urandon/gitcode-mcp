# Scenarios: 009-error-classifier-task-4-product-path-classifier-tests

## Scenario 1: 009-error-classifier-task-4-product-path-classifier-tests-scenario-1

Developer runs `go test ./internal/diagnostics/...`, `go test ./internal/service/...`, `go test ./cmd/gitcode-mcp/...`, and `go test ./...` without credentials, network, SSH agent, or Keychain.

### Product Surface

Repository-wide offline test suite against all packages.

### Steps

1. Run `go test ./internal/diagnostics/... -count=1`
2. Run `go test ./internal/service/... -count=1`
3. Run `go test ./cmd/gitcode-mcp/... -count=1`
4. Run `go test ./... -count=1`
5. Verify `git diff --check` passes

### Expected Result

All package tests pass with zero failures. No test requires credentials, network access, SSH agent, or Keychain. `git diff --check` produces no output (no whitespace violations).

### Decommission Check

For all tests exercising 400, malformed JSON, schema mismatch, partial response, or local body-limit paths: the visible failure class must never be `live_transport_failure`, `configuration_error`, `live_api_failure`, `live_auth_failure`, or `unsupported_mock_payload`.

### Scenario IDs Exercised

- `SCN-DIAG-PRODUCT-WRAP-01` through `SCN-DIAG-PRODUCT-WRAP-09`: Typed GitCode errors classify correctly when direct and wrapped by service failures (`internal/service/service_test.go`)
- `SCN-DIAG-PRECEDENCE-01` through `SCN-DIAG-PRECEDENCE-15`: Live-mode classifier precedence contract (`internal/diagnostics/classifier_test.go`)
- `SCN-DIAG-LEGACY-NORMALIZATION-01` through `SCN-DIAG-LEGACY-NORMALIZATION-04`: Legacy code normalization (`internal/diagnostics/classifier_test.go`)
- `SCN-DIAG-DECOM-01` through `SCN-DIAG-DECOM-05`: Decommissioned code invariant (`internal/diagnostics/classifier_test.go`)
- `SCN-DIAG-FAILURE-SOURCE-01` through `SCN-DIAG-FAILURE-SOURCE-04`: FailureSource mapping (`internal/diagnostics/classifier_test.go`)
- `SCN-DIAG-CODE-GITCODE-01` through `SCN-DIAG-CODE-GITCODE-07`: GitCode typed error DiagnosticCode extraction (`internal/gitcode/errors_diagnostic_test.go`)
- `SCN-CLI-ERROR-OUTPUT-400`, `SCN-CLI-ERROR-OUTPUT-401`, `SCN-CLI-ERROR-OUTPUT-404`, `SCN-CLI-ERROR-OUTPUT-409`, `SCN-CLI-ERROR-OUTPUT-413`, `SCN-CLI-ERROR-OUTPUT-429`, `SCN-CLI-ERROR-OUTPUT-MALFORMED-JSON`, `SCN-CLI-ERROR-OUTPUT-SCHEMA-MISMATCH`, `SCN-CLI-ERROR-OUTPUT-PARTIAL-RESPONSE`, `SCN-CLI-ERROR-OUTPUT-TIMEOUT`, `SCN-CLI-ERROR-OUTPUT-500`: CLI integration tests with mocked GitCode API proving canonical `failure_class` in stderr (`cmd/gitcode-mcp/main_test.go`)
- `SCN-MCP-ERROR-OUTPUT-401`, `SCN-MCP-ERROR-OUTPUT-400`, `SCN-MCP-ERROR-OUTPUT-404`, `SCN-MCP-ERROR-OUTPUT-409`, `SCN-MCP-ERROR-OUTPUT-413`, `SCN-MCP-ERROR-OUTPUT-429`, `SCN-MCP-ERROR-OUTPUT-MALFORMED-JSON`, `SCN-MCP-ERROR-OUTPUT-SCHEMA-MISMATCH`, `SCN-MCP-ERROR-OUTPUT-PARTIAL-RESPONSE`, `SCN-MCP-ERROR-OUTPUT-LOCAL-BODY-LIMIT`, `SCN-MCP-ERROR-OUTPUT-TIMEOUT`, `SCN-MCP-ERROR-OUTPUT-500`: MCP error rendering tests proving canonical `failure_class` in JSON-RPC error data (`internal/mcp/mcp_test.go`)
- `SCN-MOCKAPI-LIVE-SYNC-VALID`, `SCN-MOCKAPI-LIVE-SYNC-MISSING-CREDENTIAL`, `SCN-MOCKAPI-API-BASE-AUTHORITY`, `SCN-CLI-DOCTOR-LIVE-JSON-*`, `SCN-MOCKAPI-LIVE-CREATE-ISSUE`, `SCN-CRED-DOCTOR-LIVE-MOCK-KEYCHAIN`, `SCN-CLI-LIVE-BINDING-*`: Additional live provider product-surface integration tests without credentials, network, SSH agent, or Keychain (`cmd/gitcode-mcp/main_test.go`)

## Scenario 2: 009-error-classifier-task-4-product-path-classifier-tests-scenario-2

CLI and MCP tests exercise target product surfaces with mocked GitCode responses and prove visible failure classes are exactly `api_validation`, `schema_decode`, `config_credential`, or `live_transport_failure`.

### Product Surface

CLI `writeCommandError` via mocked `httptest.Server` GitCode API and MCP `writeDomainError` via typed error injection.

### Steps

1. Run CLI integration tests with `MockGitCodeAPI` configured for each failure mode (400, 401, 404, 409, 413, 429, malformed JSON, schema mismatch, partial response, timeout, 500)
2. Run MCP error rendering tests with typed `ErrSyncFailure`, `ErrWriteFailure`, and direct GitCode errors for each failure mode
3. Assert each test output contains exactly one canonical `failure_class` value from `{api_validation, schema_decode, config_credential, live_transport_failure}`
4. Assert decommissioned failure classes are absent from CLI stderr output for all API/schema/decode failures
5. Assert `failure_class` in MCP JSON-RPC error data matches the expected canonical class for all scenarios

### Expected Result

| Failure Mode | CLI failure_class | MCP failure_class |
|---|---|---|
| 400 Bad Request | api_validation | api_validation |
| 401 Unauthorized | api_validation | api_validation |
| 404 Not Found | api_validation | api_validation |
| 409 Conflict | api_validation | api_validation |
| 413 Payload Too Large (remote) | api_validation | api_validation |
| 429 Rate Limited | api_validation | api_validation |
| Malformed JSON | schema_decode | schema_decode |
| Schema Mismatch | schema_decode | schema_decode |
| Partial Response | schema_decode | schema_decode |
| Local Body Limit | schema_decode | schema_decode |
| Timeout | live_transport_failure | live_transport_failure |
| 500 Internal Server Error | live_transport_failure | live_transport_failure |
| Missing Credential | config_credential | config_credential |

### Decommission Check

No 400, malformed JSON, schema mismatch, partial response, or local body-limit failure renders as `live_transport_failure`, `configuration_error`, `live_api_failure`, `live_auth_failure`, or `unsupported_mock_payload` in either CLI stderr or MCP JSON-RPC error data. The `assertNoDecommissionedFailureClass` helper in `cmd/gitcode-mcp/main_test.go` codifies this invariant and is called for every API/schema/decode failure test case.

### Scenario IDs Exercised

- `SCN-CLI-ERROR-OUTPUT-400`, `SCN-CLI-ERROR-OUTPUT-401`, `SCN-CLI-ERROR-OUTPUT-404`, `SCN-CLI-ERROR-OUTPUT-409`, `SCN-CLI-ERROR-OUTPUT-413`, `SCN-CLI-ERROR-OUTPUT-429`, `SCN-CLI-ERROR-OUTPUT-MALFORMED-JSON`, `SCN-CLI-ERROR-OUTPUT-SCHEMA-MISMATCH`, `SCN-CLI-ERROR-OUTPUT-PARTIAL-RESPONSE`, `SCN-CLI-ERROR-OUTPUT-TIMEOUT`, `SCN-CLI-ERROR-OUTPUT-500`
- `SCN-MCP-ERROR-OUTPUT-401`, `SCN-MCP-ERROR-OUTPUT-400`, `SCN-MCP-ERROR-OUTPUT-404`, `SCN-MCP-ERROR-OUTPUT-409`, `SCN-MCP-ERROR-OUTPUT-413`, `SCN-MCP-ERROR-OUTPUT-429`, `SCN-MCP-ERROR-OUTPUT-MALFORMED-JSON`, `SCN-MCP-ERROR-OUTPUT-SCHEMA-MISMATCH`, `SCN-MCP-ERROR-OUTPUT-PARTIAL-RESPONSE`, `SCN-MCP-ERROR-OUTPUT-LOCAL-BODY-LIMIT`, `SCN-MCP-ERROR-OUTPUT-TIMEOUT`, `SCN-MCP-ERROR-OUTPUT-500`
- `SCN-MOCKAPI-LIVE-SYNC-MISSING-CREDENTIAL` (config_credential)
- `SCN-DIAG-DECOM-01` through `SCN-DIAG-DECOM-05`
