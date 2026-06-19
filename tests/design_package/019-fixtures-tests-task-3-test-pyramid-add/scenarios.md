# Validation Scenarios: 019-fixtures-tests-task-3-test-pyramid-add

## 019-fixtures-tests-task-3-test-pyramid-add-scenario-1

A developer triggers the offline validation path with `go test ./... -short`; the target product surfaces are cache/index/service tests, golden export tests, adapter contract tests, and MCP local integration tests, and the expected outcome is all pass in under 10 seconds without external network access.

Validation materialization:

- Execute `go test ./... -short -count=1` from the repository root with live credentials unset.
- Fail if the command exits non-zero or exceeds the offline validation budget.
- This exercises cache/index/service unit tests, fixture-backed adapter contract tests, golden export tests, and local MCP stdio integration through the Go test runtime.

## 019-fixtures-tests-task-3-test-pyramid-add-scenario-2

A developer then triggers the network-guard evidence by adding or running a test path that attempts an HTTP call to a non-loopback host under `testnet.NoExternalNetwork`; the expected outcome is a sentinel failure proving short tests reject unintended outbound network while still allowing `httptest.Server` and stdio MCP tests.

Validation materialization:

- Execute `go test ./internal/testnet/... -run '^TestNoExternalNetwork$' -count=1 -v`.
- Verify the runtime output proves `TestNoExternalNetwork` ran and passed.
- The test path must use the production `internal/testnet.NoExternalNetwork` helper, block a non-loopback URL with the sentinel error, and allow a loopback `httptest.Server` request.

## 019-fixtures-tests-task-3-test-pyramid-add-scenario-3

A developer triggers the live validation path with `go test ./... -run Integration`; the expected outcome is clean skips when `GITCODE_TEST_TOKEN` is unset, clean skips during `testing.Short()` even if the token is set, and real live API execution only when not short and the token is set.

Validation materialization:

- Execute `env -u GITCODE_TEST_TOKEN go test ./... -run Integration -count=1 -v` and require clean completion with live integration skip evidence.
- Execute `GITCODE_TEST_TOKEN=placeholder go test ./... -short -run Integration -count=1 -v` and require clean completion with short-mode live integration skip evidence.
- Inspect the live integration test surface to ensure the non-short live path is gated by `testnet.RequireLiveIntegration(t)` and consumes `GITCODE_TEST_TOKEN` for live client credentials rather than a hard-coded placeholder.
- Do not run the real token-present non-short live path because live validation is disabled for this materialization.

## 019-fixtures-tests-task-3-test-pyramid-add-scenario-4

Executable evidence: `go test ./... -short`, `go test ./... -run Integration`, and a golden export test such as `go test ./internal/cache/... -run TestGoldenExport` pass or skip as specified.

Validation materialization:

- Execute `go test ./internal/service/... -run '^TestGoldenExport$' -count=1 -v` as the current golden export product path.
- Verify verbose output proves `TestGoldenExport` ran and passed.
- The repository currently hosts the golden export evidence in `internal/service`, so this scenario accepts that equivalent cache/service export surface rather than requiring a duplicate cache-package test.
