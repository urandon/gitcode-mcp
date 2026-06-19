# Validation Scenarios: 007-gitcode-adapter-task-1-add-client-and-error-types

## 007-gitcode-adapter-task-1-add-client-and-error-types-scenario-1

A developer triggers adapter contract tests with `go test ./internal/gitcode/... -run TestContract`; the target product API is `Client.GetIssue` against an `httptest.Server` serving sanitized fixture JSON, and the expected response is a structured `Issue` with `id`, `title`, `body`, `status`, `labels`, `created_at`, and `updated_at` matching the fixture.

Validation command:

```sh
go test ./internal/gitcode/... -run '^TestContract$' -count=1
```

Expected evidence:

- The production `internal/gitcode.Client` implementation is constructed with an `httptest.Server` base URL.
- `Client.GetIssue` performs the HTTP request through the adapter path.
- The returned `Issue` contains fixture-matching `id`, `title`, `body`, `status`, `labels`, `created_at`, and `updated_at` fields.

## 007-gitcode-adapter-task-1-add-client-and-error-types-scenario-2

A developer triggers attachment coverage with `go test ./internal/gitcode/... -run TestAttachmentContract`; the target product APIs are `Client.ListIssueAttachments` and `Client.GetAttachment` against sanitized fixture responses, and the expected result is attachment metadata plus bounded attachment content, with an oversized fixture returning typed `ErrPayloadTooLarge`.

Validation command:

```sh
go test ./internal/gitcode/... -run '^TestAttachmentContract$' -count=1
```

Expected evidence:

- `Client.ListIssueAttachments` returns attachment metadata from a local `httptest.Server` response.
- `Client.GetAttachment` returns bounded attachment content and response metadata.
- Oversized attachment content fails with typed `ErrPayloadTooLarge`.

## 007-gitcode-adapter-task-1-add-client-and-error-types-scenario-3

A developer triggers retry coverage with `go test ./internal/gitcode/... -run TestReadRetry`; the target routes include one read route returning `429` then `200` under the same context and producing the successful record, and one list route exhausting `429` retries and returning `ErrRateLimited{RetryAfter}` with no records.

Validation command:

```sh
go test ./internal/gitcode/... -run '^TestReadRetry$' -count=1
```

Expected evidence:

- A read route that initially returns `429` is retried within the caller context and eventually returns a successful issue.
- A list route that exhausts `429` retries returns a typed rate-limit/network-bounded error and no partial records.
- Retry behavior remains offline and deterministic.

## 007-gitcode-adapter-task-1-add-client-and-error-types-scenario-4

A developer triggers timeout and failure coverage with `go test ./internal/gitcode/... -run 'TestTimeout|TestFailureModes'`; routes exercise 401, 403, 404, 409, 429 with valid and invalid `Retry-After`, 5xx/network failures, malformed JSON, truncated JSON, and max-size truncation, with 403 returning `ErrForbidden` stable fields and non-retry permission guidance, network timeout returning `ErrNetworkUnavailable` containing endpoint context and retry guidance, and rate limit returning `ErrRateLimited.RetryAfter`.

Validation command:

```sh
go test ./internal/gitcode/... -run '^(TestTimeout|TestFailureModes)$' -count=1
```

Expected evidence:

- Timeout behavior returns typed `ErrNetworkUnavailable` with endpoint context and retry guidance.
- HTTP failure statuses map to stable typed adapter errors.
- Malformed/truncated/oversized responses fail as degradation cases rather than partial successes.
- Forbidden responses expose non-retry permission guidance.
