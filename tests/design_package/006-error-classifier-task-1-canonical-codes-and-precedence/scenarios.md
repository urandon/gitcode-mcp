# Scenarios: 006-error-classifier-task-1-canonical-codes-and-precedence

## Scenario 1
`006-error-classifier-task-1-canonical-codes-and-precedence-scenario-1`: Developer runs `go test ./internal/diagnostics/...`; table-driven runtime tests exercise live-http classifier precedence for configuration invalidity, missing credential, invalid API base URL, remote HTTP 401, remote HTTP 400, malformed 200 JSON, schema mismatch, local body-limit, remote 413, partial response, timeout, and HTTP 500. Each case must return the expected canonical visible class: `config_credential`, `api_validation`, `schema_decode`, or `live_transport_failure`, and decommissioned legacy visible classes must not be returned for 400/schema/decode failures.

**Executable coverage:**
- `SCN-DIAG-PRECEDENCE-01` through `SCN-DIAG-PRECEDENCE-12` are executed by `TestClassifierLivePrecedenceAndHTTPInvariants` in `internal/diagnostics/classifier_test.go`.
- `SCN-DIAG-DECOM-01` is executed by `TestClassifierLiveDecommissionInvariant` and verifies 400/schema/decode-like failures do not render as transport, configuration, or legacy live classes.
- `SCN-DIAG-LEGACY-NORMALIZATION-01` is executed by `TestClassifierLegacyCodeNormalization` and verifies legacy live codes normalize to canonical classes.
- `SCN-DIAG-FAILURE-SOURCE-01` is executed by `TestClassifierFailureSourceMapping` and verifies remote payload-size status maps to `api_validation` while local/decode-boundary failures map to `schema_decode`.

## Scenario 2
`006-error-classifier-task-1-canonical-codes-and-precedence-scenario-2`: MCP and CLI product-path tests backed by mocked GitCode failures show visible `failure_class: api_validation`, `schema_decode`, `config_credential`, or `live_transport_failure`; regression tests fail if 400/schema/decode failures are rendered as transport or configuration failures.

**Executable coverage:**
- `SCN-CLI-ERROR-OUTPUT-01` is executed by the `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-SYNC-INVALID-TOKEN-401 SCN-CLI-ERROR-OUTPUT-01` subtest in `cmd/gitcode-mcp/main_test.go`. It runs the production CLI `sync --live` path against a mocked GitCode API and asserts stderr/stdout exposes `failure_class: api_validation`.
- `SCN-MCP-ERROR-OUTPUT-01` is executed by `TestMCPErrorOutputCanonicalFailureClass` in `internal/mcp/mcp_test.go`. It runs production MCP JSON-RPC error rendering for a live-origin domain error and asserts error data exposes `failure_class: api_validation`.
- The validation script fails when either product-path subtest is absent, compiles but does not run, or returns a non-canonical visible failure class.
