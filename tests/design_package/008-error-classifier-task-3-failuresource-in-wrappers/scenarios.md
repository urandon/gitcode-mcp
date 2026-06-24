# Validation Scenarios: FailureSource in wrappers

Task: `008-error-classifier-task-3-failuresource-in-wrappers`

## 008-error-classifier-task-3-failuresource-in-wrappers-scenario-1

Developer runs `go test ./internal/diagnostics/...` and this validation package. The validation runtime test exercises production `diagnostics.Classify` with production `gitcode.ErrPayloadTooLarge` and `gitcode.ErrPartialResponse` errors:

- `gitcode.ErrPayloadTooLarge{Source:"remote_status"}` with HTTP 413 and `HTTPAttempted: true` returns visible class `api_validation`.
- `gitcode.ErrPayloadTooLarge{Source:"local_body_limit"}` with successful or unknown status returns visible class `schema_decode`.
- `gitcode.ErrPartialResponse` after an attempted live HTTP response returns visible class `schema_decode`.
- None of those payload-size, decode-boundary, or partial-response cases returns the decommissioned `live_transport_failure`, `configuration_error`, `live_api_failure`, `live_auth_failure`, or `unsupported_mock_payload` visible classes.

## 008-error-classifier-task-3-failuresource-in-wrappers-scenario-2

Developer runs service-level and product-path validation tests with local typed failures standing in for mocked GitCode-client results. The validation runtime test exercises production `service.ErrSyncFailure`, `service.ErrWriteFailure`, CLI diagnostic rendering, and MCP diagnostic rendering paths:

- `service.ErrSyncFailure` preserves `PayloadSource` from an underlying `gitcode.ErrPayloadTooLarge{Source:"local_body_limit"}` while `errors.As` still reaches the underlying GitCode error.
- CLI diagnostic rendering uses the preserved wrapper metadata so local body-limit failures render `failure_class: schema_decode` rather than `live_transport_failure`.
- MCP diagnostic rendering uses the preserved wrapper metadata so local body-limit write failures render `failure_class: schema_decode` rather than `live_transport_failure`.
- Remote payload status failures remain `api_validation`, local payload-size failures remain `schema_decode`, and no payload-size case is rendered as `live_transport_failure` unless it is a true transport failure.
