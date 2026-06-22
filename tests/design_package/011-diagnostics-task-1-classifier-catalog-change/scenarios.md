# Materialized Validation Scenarios: 011-diagnostics-task-1-classifier-catalog-change

## `011-diagnostics-task-1-classifier-catalog-change-scenario-1`

Operator runs `gitcode-mcp sync --live` with no usable credential through the CLI command surface. The visible error must report `missing_credential`, the command must fail, and the diagnostics unit coverage must prove missing-credential precedence before invalid selected base URL unless broader live configuration is invalid.

## `011-diagnostics-task-1-classifier-catalog-change-scenario-2`

Operator runs `gitcode-mcp sync --live` with both no credential and an invalid selected base URL. The CLI/diagnostics surface must report `missing_credential` unless repository binding, cache, audit, or another required live-mode setting is already invalid and therefore classified as `configuration_error`.

## `011-diagnostics-task-1-classifier-catalog-change-scenario-3`

Operator runs `gitcode-mcp sync --live` with an invalid token against a mock server returning 401 or 403. The product diagnostics path must report `live_auth_failure`, preserve `http_attempted=true`, prove the live provider route was attempted, and avoid fixture success output.

## `011-diagnostics-task-1-classifier-catalog-change-scenario-4`

Operator runs `gitcode-mcp create-issue --live` and write-service supplies a live-route fixture read-only sentinel. The CLI diagnostics path must report `fixture_fallback_detected` rather than the raw `fixture client is read-only` message; non-live fixture write behavior may still classify as `fixture_read_only`.

## `011-diagnostics-task-1-classifier-catalog-change-scenario-5`

Operator runs a live CLI route with missing repository binding, missing or invalid cache path, missing or invalid audit path for live create, or another required live-mode setting. The diagnostics classifier must report `configuration_error` with `http_attempted=false`, including precedence over missing credentials.

## `011-diagnostics-task-1-classifier-catalog-change-scenario-6`

Operator runs `sync --live` with an invalid selected live API base URL and otherwise valid live startup inputs. The diagnostics classifier must report `invalid_api_base_url` before HTTP after broader configuration and missing credential checks pass.

## `011-diagnostics-task-1-classifier-catalog-change-scenario-7`

System injects a malformed mock/live payload missing a required issue, wiki, comment, or create identifier through the provider validation path. The diagnostics classifier must report `unsupported_mock_payload` with redacted context and no secret-bearing payload details.

## Executable Validation

`run.sh` executes offline Go tests that cover the diagnostics classifier catalog, live error precedence, provider attempted-request/auth behavior, selected-only base URL routing, write-service fixture fallback sentinels, and full repository test compatibility under `go test ./...`.
