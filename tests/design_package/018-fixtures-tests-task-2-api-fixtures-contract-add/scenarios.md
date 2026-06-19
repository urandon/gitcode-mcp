# Validation Scenarios: 018-fixtures-tests-task-2-api-fixtures-contract-add

## 018-fixtures-tests-task-2-api-fixtures-contract-add-scenario-1

A developer triggers the adapter evidence path by running `go test ./internal/gitcode/... -run TestContract`.

Validation materialization:

- Execute the product evidence through the Go test runtime with `go test ./internal/gitcode/... -run '^TestContract$' -count=1` from the repository root.
- Treat any non-zero exit as a task failure.
- Verify this scenario id remains present so the harness is traceable to the acceptance criterion.

## 018-fixtures-tests-task-2-api-fixtures-contract-add-scenario-2

The target surface is the GitCode adapter API exercised through a local HTTP server; the expected response is structured issue records matching fixture fields, with no outbound network and no private fixture content.

Validation materialization:

- Require endpoint-shaped sanitized JSON fixtures for issue list, single issue, issue comments, wiki page list, and single wiki page under `fixtures/api/v5/repos/example-owner/example-repo/`.
- Verify the required fixture files are non-empty valid JSON and contain no `Authorization`, raw owner/repo/project tokens, raw hostnames, or hostnames other than `api.example.com`.
- Verify the contract test source contains `httptest.NewServer` and exercises the production `internal/gitcode` client methods for issue list, single issue, issue comments, wiki list, and wiki page retrieval.
- Run `go test ./internal/gitcode/... -run '^TestSanitizedFixtures$' -count=1 -v` to validate the committed fixture corpus public-safety guard.

## 018-fixtures-tests-task-2-api-fixtures-contract-add-scenario-3

Executable evidence: `go test ./internal/gitcode/... -run TestContract` passes and includes list-issue, single-issue, and timeout scenarios backed only by sanitized fixtures and local `httptest.Server`.

Validation materialization:

- Run `go test ./internal/gitcode/... -run '^(TestContract|TestTimeout)$' -count=1 -v` to execute the contract and typed-timeout evidence in the adapter package.
- Verify verbose output proves both `TestContract` and `TestTimeout` executed and passed.
- Verify the timeout test source asserts typed `ErrNetworkUnavailable`.
