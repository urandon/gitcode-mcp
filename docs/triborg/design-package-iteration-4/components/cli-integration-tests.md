# Design Package Component: cli-integration-tests

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: CLI Integration Tests

## Summary
CLI integration tests are affected because iteration 4 requires executable offline evidence that explicit live commands traverse the real operator startup path. This component validates behavior owned by startup, provider, credential, repository binding, cache, audit, and doctor components without moving product logic into the tests.

## Top-Level Alignment
This component implements the approved architecture’s offline `go test ./...` acceptance layer. It exercises `cmd/gitcode-mcp/` real command startup, or the `internal/cli` execution seam when it is the project’s equivalent command wrapper, with temporary runtime state and mocked external GitCode HTTP behavior.

## Tasks

### Task 1: Validate real CLI live routes
Outcome IDs: outcome-1, outcome-2, outcome-3, outcome-4, outcome-5, outcome-6, outcome-7, outcome-8, outcome-9, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-1, decommission-2, decommission-3, decommission-4
Change Type: validate
Description: Add an offline real-CLI integration suite that drives `sync --live`, `create-issue --live`, `auth status`, default `sync`, and `doctor --live --format json` through the operator startup boundary. The suite is the component-local evidence boundary for provider wiring, credential source consistency, API base URL authority, cache/audit reconciliation, and fixture-fallback rejection. It must validate product behavior through CLI execution and runtime state, not by isolated provider unit tests.
Existing Behavior / Reuse: Reuse `cmd/gitcode-mcp/` startup tests and the `run`/startup dependency handoff concept for real entrypoint coverage; reuse `internal/cli` command execution tests only when they represent the project’s equivalent command wrapper. Reuse existing `internal/cache` runtime APIs for source records, comments, sync status/events, and create confirmations; reuse `internal/audit` runtime APIs for non-secret audit confirmations; reuse `internal/provider/live` and existing `httptest` conventions only as consumed dependencies. Existing tests cover entrypoint handoff, CLI command behavior, doctor construction, credential status, cache store behavior, audit persistence, and provider/client `httptest` cases, but they do not yet provide one offline real-CLI live-readiness suite combining temporary config/cache/audit, mocked credentials, request counters, fixture leakage rejection, selected-only base URL routing, and no-OS-Keychain/no-real-network controls.
Detailed Design: Add a CLI integration harness in the existing Go test layout anchored at `cmd/gitcode-mcp/` for real startup-path tests; if startup cannot directly host every assertion, delegate command execution through the existing `internal/cli` execution seam while preserving operator-style argv, isolated stdin/stdout/stderr buffers, exit status capture, and startup dependency construction. The harness should provide named helpers for: creating temporary configuration and repository binding records, setting a temporary cache path, setting a temporary audit path, running argv through the real startup route, reading cache state through `internal/cache` runtime APIs, reading audit state through `internal/audit` runtime APIs, parsing doctor JSON, and asserting per-operation mock request counts. Product logic remains in `cli-startup`, `credential-resolution`, `repository-binding`, `live-provider`, `sync-service`, `write-service`, `doctor-readiness`, `cache-runtime`, and `audit-runtime`; this component only orchestrates commands and assertions.

The harness setup must scrub real credential environment variables, including `GITCODE_TOKEN` and any configured GitCode credential aliases, except intentional per-test values. It must configure or disable native OS Keychain access for these tests by selecting a test-only mocked Keychain-equivalent credential source and verifying no primary validation path depends on platform credential state. Each live test must bind repository `api_base_url` to an `httptest.Server` URL; tests that exercise base URL authority must also configure at least one non-selected/default endpoint and assert its request count stays zero or that it fails if contacted. Temporary cache, audit, home, config, and cache-dir values must be isolated per test, sanitized, and public-safe.

The mocked GitCode API consumed by the suite must expose sanitized issue, wiki, comment, create-issue, auth-failure, and request-counter behavior through `httptest.Server`. Valid live sync scenarios assert that `sync --live` sends authenticated requests to the selected mock server, writes mock issue/wiki/comment records into the configured cache, records sync events or cache confirmations, and excludes `ISSUE-42` and `WIKI-HOME` from live output and live cache state. Missing-credential scenarios assert a typed `missing_credential` diagnostic, expected non-zero failure status, and zero mock requests. Invalid-token scenarios assert the server receives at least one request and CLI output reports `live_auth_failure` or the equivalent typed auth failure without fixture success.

The credential-divergence scenario must run `auth status` with `GITCODE_TOKEN` unset and the mocked Keychain-equivalent source configured, then run `create-issue --live` in the same temporary configuration. The test must fail if `auth status` reports the mocked credential source but live create does not authenticate to the mock server using that same resolved source. The create issue scenarios must assert POST/create request receipt, expected idempotency behavior, create confirmation state in cache, and a non-secret audit confirmation record through the target runtime APIs.

The default sync scenario must run `gitcode-mcp sync` without `--live` while a mock server is available and assert fixture-backed offline behavior with zero mock HTTP requests. The doctor scenario must run `doctor --live --format json` and assert effective provider mode `live-http`, non-secret credential source metadata, selected cache path, and selected API base URL match the startup configuration used by live commands without leaking token material. For decommissioning, the suite does not delete fixture runtime support; it moves forbidden live-route behavior into negative integration assertions: explicit live sync fixture fallback, invalid-token fixture success, live sync cache population from default fixtures, and live create issue `fixture client is read-only` all fail the suite if observed.
Acceptance Criteria: Developer runs `go test ./...`; target product surface is the real `gitcode-mcp` CLI startup path plus cache/audit runtime inspection; expected outcome is that all live-readiness integration scenarios pass offline without real credentials, external network, or OS Keychain access. Operator-equivalent triggers include `gitcode-mcp sync --live`, `gitcode-mcp create-issue --live`, `gitcode-mcp auth status`, `gitcode-mcp sync`, and `gitcode-mcp doctor --live --format json`; expected visible/state outcomes include admitted live mock requests, typed missing-credential with zero requests, live auth failure with nonzero requests, mock issue/wiki/comment records and sync events or cache confirmations, create confirmation state, non-secret audit confirmation records, selected-only base URL hits, default fixture sync with zero mock hits, doctor JSON effective fields, and failure on `ISSUE-42`, `WIKI-HOME`, fixture success, or `fixture client is read-only` in live output/state. Executable evidence is the stubbed-external-provider Go integration test suite using `httptest.Server` while exercising the target CLI/runtime path.
Workload: 0.8 MM

## Cross-Cutting Constraints
- Real CLI route coverage — integration evidence must enter through `cmd/gitcode-mcp/` startup or the equivalent `internal/cli` wrapper so provider composition defects cannot be hidden by provider-only unit tests
- Offline-only validation — tests must use `httptest.Server`, temporary config/cache/audit paths, scrubbed credential environment, and mocked credential sources without real credentials, external network, or OS Keychain
- Public-safe runtime evidence — mock payloads and assertions must use sanitized identifiers and reject fixture leakage on explicit live routes
- Supporting-evidence ownership — this component validates behavior owned by other product components without relocating their runtime logic into tests

## Data And Control Flow
- Test harness creates temporary runtime inputs — `cli-integration-tests` — config, repository binding, cache path, audit path, mocked credential source, selected `httptest.Server` base URL, and non-selected endpoint counters are established before command invocation
- Real CLI startup runs command — `cli-integration-tests` and `cli-startup` — argv such as `sync --live`, `create-issue --live`, `auth status`, or `doctor --live --format json` flows through startup composition before services execute
- Mock server records external interactions — `live-provider` and `mock-gitcode-api` — request counters prove selected endpoint usage, zero-request missing credential, nonzero-request auth failure, and zero requests to non-authoritative endpoints
- Runtime state is inspected after command completion — `cache-runtime` and `audit-runtime` — tests read issue/wiki/comment records, sync events or cache confirmations, create confirmation state, and non-secret audit confirmations through runtime APIs
- Suite result gates acceptance — `go test ./...` — all scenarios pass in one offline local test command without external dependencies

## Component Interactions
- `cli-integration-tests` -> `cli-startup` — invokes the real `cmd/gitcode-mcp/` startup route or equivalent command wrapper with operator-style argv and isolated IO buffers
- `cli-integration-tests` -> `mock-gitcode-api` — consumes sanitized `httptest.Server` endpoints, request counters, auth modes, and mock payloads as external dependency substitutes
- `cli-integration-tests` -> `credential-resolution` — supplies scrubbed environment and mocked Keychain-equivalent configuration, then asserts `auth status` and live write consume the same non-secret credential source metadata
- `cli-integration-tests` -> `repository-binding` — supplies temporary repository binding with authoritative `api_base_url` and verifies selected-only routing through server counters
- `cli-integration-tests` -> `cache-runtime` — inspects issue, wiki, comment, sync event/cache confirmation, and create-confirmation state after CLI commands through runtime APIs
- `cli-integration-tests` -> `audit-runtime` — inspects non-secret live create issue audit confirmation after the product write path completes
- `cli-integration-tests` -> `doctor-readiness` — validates live JSON fields for effective provider mode, credential source, cache path, and API base URL

## Rationale
The approved architecture makes CLI integration tests the acceptance boundary for iteration 4 because the main risk is false live readiness from helper paths, fixture fallback, divergent credential sources, or provider-only tests. A single real-CLI offline suite covers the configured component delta and provides supporting evidence for all decomposed request tasks.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0269-run_attempt-1/final_message.txt`
