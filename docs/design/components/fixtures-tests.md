# Component Design: Fixtures And Tests

## Summary
The fixtures-tests component creates the public-safe evidence layer for GitCode API behavior and the repository-wide test pyramid. The current repository has only a CLI scaffold test and a generic check script, with no fixture corpus, sanitizer, GitCode adapter contract tests, golden tests, MCP tests, or credential-gated live integration tests.

## Top-Level Alignment
This component supports the approved cache-first architecture by ensuring adapter behavior is verified from sanitized local fixtures, routine tests run offline, deterministic outputs are golden-tested, and live GitCode calls are opt-in only. It provides evidence for `internal/gitcode`, `internal/cache`, `internal/index`, `internal/service`, `internal/mcp`, and `internal/cli` without owning their runtime implementation.

## Tasks

### Task 1: Sanitizer Script Add
Outcome IDs: outcome-7
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Add the fixture sanitizer as the component-local ingestion boundary between raw GitCode API captures and committed public fixtures. The sanitizer owns redaction of credential headers, hostnames, and private owner/repo/project identifiers before any fixture is consumed by adapter tests. Sanitized fixtures become the only committed API evidence used by contract tests.
Existing Behavior / Reuse: Confirmed absent: there is no `fixtures/` tree and no sanitizer script; the only existing script is `scripts/check.sh`, which sets `GOCACHE`, runs `go test ./...`, and runs `git diff --check`. Reuse the existing shell-script convention: strict Bash with `set -euo pipefail` and repository-root execution.
Detailed Design: Add `scripts/sanitize-fixtures.sh` as a strict Bash command that accepts exactly: `<raw_dir> <output_dir> --owner <raw_owner> --repo <raw_repo> --project <raw_project> --host <raw_host> [--host <raw_host>...]`. The script recursively processes JSON, plain text, and HTTP transcript files from `<raw_dir>`, preserves endpoint-shaped relative paths under `<output_dir>/api/v5/`, and applies deterministic replacements in both file contents and output paths: every configured owner token becomes `example-owner`, repo token becomes `example-repo`, project token becomes `example-project`, and configured host token becomes `api.example.com`. The credential invariant is unambiguous: any line whose case-insensitive header key is `Authorization` is removed entirely from sanitized output, so no sanitized fixture contains the literal string `Authorization`; inline JSON or transcript fields named `Authorization` are removed if structurally identifiable, otherwise the script fails rather than emitting them. Add a sanitizer verification test entity in the `internal/gitcode` test surface named `TestSanitizedFixtures` that walks `fixtures/` recursively and fails if any file path or content contains `Authorization`, a configured raw owner/repo/project token, a configured raw hostname, or a hostname other than `api.example.com` in API-origin fields. The invariant is that raw captures remain outside product runtime and outside committed test inputs, while contract tests only read sanitized fixture files.
Acceptance Criteria: A developer triggers the fixture-publication path by running `scripts/sanitize-fixtures.sh <raw_dir> fixtures/ --owner raw-owner --repo raw-repo --project raw-project --host gitcode.example.invalid` and then running `go test ./internal/gitcode/... -run TestSanitizedFixtures`. The target surface is the fixture sanitizer plus the sanitized fixture corpus; the expected state is that output files preserve endpoint-shaped paths while containing only `api.example.com`, `example-owner`, `example-repo`, and `example-project` placeholders and no `Authorization` string, raw credentials, raw hostnames, or private identifiers. Executable evidence: the sanitizer command exits 0 and `go test ./internal/gitcode/... -run TestSanitizedFixtures` passes; if a raw `Authorization` header survives in any output file, the test fails.
Workload: 0.4 MM

### Task 2: API Fixtures Contract Add
Outcome IDs: outcome-3, outcome-7
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: add
Description: Add the sanitized API fixture corpus and adapter contract tests that prove the GitCode adapter can parse issue and related API responses without network access. This task owns the fixture shape and the contract-test harness, not the adapter production implementation. Contract tests exercise the adapter HTTP path through a local server rather than parsing fixture files directly.
Existing Behavior / Reuse: Confirmed absent: there is no `internal/gitcode` package, no adapter fixture corpus, and no contract-test harness. Reuse Go standard `testing`, `httptest.Server`, and sanitized outputs from `scripts/sanitize-fixtures.sh` as the only test data source.
Detailed Design: Add a fixture corpus organized by GitCode `/api/v5` endpoint concepts: issue list, single issue, issue comments, wiki page list, and single wiki page, using only `example-owner`, `example-repo`, `example-project`, and `api.example.com` placeholder identities. The fixture layout must include endpoint-shaped files for repository issue listing, single issue fetch by number, issue comments, wiki page listing, and single wiki page fetch; each file is a sanitizer output, not hand-copied raw capture. Add adapter contract tests in the `internal/gitcode` test surface that serve fixture files through `httptest.Server`, configure the adapter base URL to that local server, call adapter methods such as `ListIssues` and `GetIssue`, and assert structured fields including id, title, body, status/state, labels, created time, and updated time. The test harness must also include a timeout scenario using a local slow `httptest.Server` and a short context deadline, asserting the adapter returns typed `ErrNetworkUnavailable`. The fixture invariant is that every contract test reads only from the sanitized fixture corpus and only contacts loopback `httptest.Server`.
Acceptance Criteria: A developer triggers the adapter evidence path by running `go test ./internal/gitcode/... -run TestContract`. The target surface is the GitCode adapter API exercised through a local HTTP server; the expected response is structured issue records matching fixture fields, with no outbound network and no private fixture content. Executable evidence: `go test ./internal/gitcode/... -run TestContract` passes and includes list-issue, single-issue, and timeout scenarios backed only by sanitized fixtures and local `httptest.Server`.
Workload: 0.5 MM

### Task 3: Test Pyramid Add
Outcome IDs: outcome-8
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Add the repository-wide test pyramid conventions and executable tests for offline unit/contract/golden coverage, MCP local integration, and credential-gated live integration. This component-level task creates the evidence structure that keeps routine validation cache-first and network-free. It also defines the exact live-test skip rules so routine short tests never perform network calls even when credentials exist.
Existing Behavior / Reuse: Existing behavior is limited to `internal/cli/cli_test.go`, which covers help output and stub command behavior, plus `scripts/check.sh`, which runs `go test ./...` and `git diff --check`. There are no golden tests, no short/offline network guard, no MCP integration tests, and no live integration gate. Reuse Go `testing`, in-memory SQLite for cache-oriented tests, local stdio pipes for MCP tests, and `httptest.Server` for external API substitution.
Detailed Design: Add unit tests for cache/index/service behavior that use in-memory SQLite and no network, golden tests that compare deterministic export output with byte-identical expected data, adapter contract tests that reuse the sanitized fixture harness, and MCP integration tests that start the stdio server and issue JSON-RPC `tools/list` and `tools/call` requests over local pipes. Add a package-local test helper concept, `testnet.NoExternalNetwork`, used by short/offline tests that install an HTTP transport guard for code paths under test: requests to `127.0.0.1`, `localhost`, and `httptest.Server` URLs are allowed; any request to a non-loopback host returns a sentinel error and causes the test to fail. The short-test invariant is that `go test ./... -short` uses only local data, local transports, in-memory SQLite, golden files, stdio pipes, and loopback `httptest.Server`; unintended outbound HTTP in short tests is a hard failure, not a convention. Live integration tests must be named with `Integration`, must call a shared `RequireLiveIntegration(t)` helper at the start, and that helper must skip when `testing.Short()` is true, skip when an explicit integration run is absent from the test name selection, skip when `GITCODE_TEST_TOKEN` is unset, and run live API paths only when not short and the token is present. Expected behavior is: `go test ./... -short` always skips live tests and fails on unintended outbound network; `go test ./... -run Integration` without `GITCODE_TEST_TOKEN` skips cleanly with `t.Skip`; `GITCODE_TEST_TOKEN=... go test ./... -run Integration` executes live API calls and reports real pass/fail.
Acceptance Criteria: A developer triggers the offline validation path with `go test ./... -short`; the target product surfaces are cache/index/service tests, golden export tests, adapter contract tests, and MCP local integration tests, and the expected outcome is all pass in under 10 seconds without external network access. A developer then triggers the network-guard evidence by adding or running a test path that attempts an HTTP call to a non-loopback host under `testnet.NoExternalNetwork`; the expected outcome is a sentinel failure proving short tests reject unintended outbound network while still allowing `httptest.Server` and stdio MCP tests. A developer triggers the live validation path with `go test ./... -run Integration`; the expected outcome is clean skips when `GITCODE_TEST_TOKEN` is unset, clean skips during `testing.Short()` even if the token is set, and real live API execution only when not short and the token is set. Executable evidence: `go test ./... -short`, `go test ./... -run Integration`, and a golden export test such as `go test ./internal/cache/... -run TestGoldenExport` pass or skip as specified.
Workload: 0.8 MM

## Cross-Cutting Constraints
- Sanitized fixtures are the only committed API evidence — adapter contract tests must prove GitCode behavior without credentials, internal URLs, or private project names.
- Routine validation is offline-first — short tests must use local fixtures, in-memory stores, golden files, and local transports instead of live network access.
- Live integration is credential-gated and short-test-safe — tests that contact GitCode run only when explicitly selected, not in `testing.Short()`, and only when `GITCODE_TEST_TOKEN` is present.

## Data And Control Flow
- Raw capture directory -> sanitizer -> sanitized fixture corpus — the sanitizer removes `Authorization` lines, maps configured identifiers to placeholders, and owns redaction before fixtures become test inputs.
- Sanitized fixture corpus -> local HTTP test server -> GitCode adapter contract tests — contract tests exercise the adapter HTTP path while remaining offline.
- Test trigger -> short/offline tests or credential-gated integration tests — `-short` uses no external network and `Integration` uses live API only with explicit selection plus token.

## Component Interactions
- `scripts/sanitize-fixtures.sh` -> `fixtures/` — produces public-safe API response files with placeholder identities and no `Authorization` header lines for all adapter contract tests.
- `fixtures/` -> `internal/gitcode` tests — supplies sanitized `/api/v5` responses served by `httptest.Server` to validate adapter parsing and typed network errors.
- `test pyramid` -> `internal/cache`, `internal/index`, `internal/service`, `internal/mcp`, `internal/cli` — defines package-local tests, golden files, local transports, network guard helpers, and integration gates used by each component owner.

## Rationale
The component is affected because the approved architecture explicitly assigns fixture sanitization, fixture layout, contract tests, golden tests, MCP tests, no-network short validation, and live integration gating to the fixtures-tests impact area. These tasks are concrete and currently absent from the repository, so `Decision: detailed` is required.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0242-run_attempt-1/final_message.txt`
