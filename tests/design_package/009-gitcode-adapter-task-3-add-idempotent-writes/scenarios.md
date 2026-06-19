# Validation Scenarios: 009-gitcode-adapter-task-3-add-idempotent-writes

## 009-gitcode-adapter-task-3-add-idempotent-writes-scenario-1

A developer triggers write tests with `go test ./internal/gitcode/... -run TestWriteIdempotency`; the target product API is `Client.CreateIssue` against an `httptest.Server`, and the captured HTTP request visibly includes an `Idempotency-Key` header and JSON payload generated through `createIssueEndpoint`.

Executable validation: `run.sh` runs `go test ./internal/gitcode/... -run '^TestWriteIdempotency$/^sends idempotency key and JSON payload$' -count=1` from the repository root.

## 009-gitcode-adapter-task-3-add-idempotent-writes-scenario-2

When the server returns 409 Conflict, the expected response is typed `ErrConflict` containing both the local request payload and remote response payload, with no automatic overwrite and no successful record returned.

Executable validation: `run.sh` runs `go test ./internal/gitcode/... -run '^TestWriteIdempotency$/^conflict returns local and remote payloads$' -count=1` from the repository root.

## 009-gitcode-adapter-task-3-add-idempotent-writes-scenario-3

When the server returns 429 followed by 201, the expected result is a structured created issue and the server observes the exact same idempotency key on every retry attempt; when the caller supplies `WriteOptions.IdempotencyKey`, the server observes that exact key for deterministic replay.

Executable validation: `run.sh` runs `go test ./internal/gitcode/... -run '^TestWriteIdempotency$/^retry preserves key and replay option$' -count=1` from the repository root.

## 009-gitcode-adapter-task-3-add-idempotent-writes-scenario-4

A developer triggers route coverage with `go test ./internal/gitcode/... -run TestWriteUsesEndpointBuilders`; the target APIs include `CreateIssue` plus at least one label or wiki write route, and the expected result is that requests pass through the centralized builders from Task 2.

Executable validation: `run.sh` runs `go test ./internal/gitcode/... -run '^TestWriteUsesEndpointBuilders$' -count=1` from the repository root.

## Regression Guard

`run.sh` also runs `go test ./internal/gitcode/... -run '^(TestWriteIdempotency|TestWriteUsesEndpointBuilders|TestWriteEndpointsTemplate|TestReadRetry|TestFailureModes)$' -count=1` so idempotent write behavior remains compatible with centralized endpoint templates, retry handling, and typed failure mapping.
