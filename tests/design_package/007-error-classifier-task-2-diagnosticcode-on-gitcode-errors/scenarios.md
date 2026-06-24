# Validation Scenarios: DiagnosticCode on GitCode errors

Task: `007-error-classifier-task-2-diagnosticcode-on-gitcode-errors`

## 007-error-classifier-task-2-diagnosticcode-on-gitcode-errors-scenario-1

Developer runs `go test ./internal/diagnostics/...` and a validation runtime test with wrapped and unwrapped GitCode typed errors. The runtime test exercises production `gitcode.ErrNotFound`, `ErrConflict`, `ErrRemoteCollision`, `ErrRemoteNotFound`, `ErrPayloadTooLarge`, `ErrPartialResponse`, and `ErrRateLimited` values. For each named type it verifies:

- `DiagnosticCode()` returns the expected task-defined string.
- `diagnostics.Classify` sees the same code through direct and `fmt.Errorf("wrapped: %w", err)` paths.
- live-http classification is canonical: API validation errors classify as `api_validation`, partial/local decode failures classify as `schema_decode`, and no target case returns a decommissioned visible class.

## 007-error-classifier-task-2-diagnosticcode-on-gitcode-errors-scenario-2

Developer runs `go test ./internal/gitcode/...`; existing GitCode error-message behavior remains unchanged. The validation runtime test additionally checks the public `Error()` text for each target typed error against the current product contract while preserving the package-level GitCode test suite as the authoritative regression gate.
