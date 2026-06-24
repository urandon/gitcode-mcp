# Outcome Contract

Schema Version: `triborg.outcome-contract.v1`

## outcome-1
- Request Task: 1
- Role: `supporting_evidence`
- Request Item: Explicit `sync --live` must reach the live provider through the same CLI/startup path an operator uses.
- Target Surface: CLI command `gitcode-mcp sync --live`
- Actor/Trigger: Operator runs sync with `--live` and usable test credentials against a mocked GitCode API.
- Expected Outcome: Live provider is constructed and mock server receives requests; fixture identifiers are absent.
- Evidence Type: CLI command
- Freshness: Current source and test binary from the target repository.
- Mock Policy: external_dependencies_only


## outcome-2
- Request Task: 2
- Role: `supporting_evidence`
- Request Item: `sync --live` with no usable credential fails with typed missing-credential diagnostics and makes no mock-server requests.
- Target Surface: CLI command `gitcode-mcp sync --live`
- Actor/Trigger: Operator runs sync with `--live` and no environment or mocked credential.
- Expected Outcome: Typed missing-credential diagnostic, expected failure status, and zero HTTP requests.
- Evidence Type: CLI command
- Freshness: Current target test run with fresh temporary config and cache.
- Mock Policy: external_dependencies_only


## outcome-3
- Request Task: 3
- Role: `supporting_evidence`
- Request Item: A mocked credential source equivalent to Keychain can satisfy live write credential checks with `GITCODE_TOKEN` unset.
- Target Surface: Credential resolution and CLI command `gitcode-mcp create-issue --live`
- Actor/Trigger: Operator runs create issue with `GITCODE_TOKEN` unset and a mocked Keychain-equivalent credential source.
- Expected Outcome: Resolved credential is consumed by the live write path and the mock server receives the authenticated request.
- Evidence Type: CLI command
- Freshness: Current target runtime test without OS Keychain access.
- Mock Policy: explicit_allowed_mocks


## outcome-4
- Request Task: 4
- Role: `supporting_evidence`
- Request Item: `sync --live` with an invalid token reaches the mocked live provider and reports 401/403 auth failure, not fixture success.
- Target Surface: CLI command `gitcode-mcp sync --live`
- Actor/Trigger: Operator runs sync with an invalid token against a mock server returning 401 or 403.
- Expected Outcome: CLI reports live auth failure and does not report fixture success.
- Evidence Type: CLI command
- Freshness: Current test binary and fresh mock server run.
- Mock Policy: external_dependencies_only


## outcome-5
- Request Task: 5
- Role: `supporting_evidence`
- Request Item: `sync --live` with a valid test token populates cache from mocked issue/wiki/comment responses and does not produce fixture records.
- Target Surface: CLI command `gitcode-mcp sync --live` and cache runtime
- Actor/Trigger: Operator runs live sync with a valid test token against sanitized mock issue/wiki/comment endpoints.
- Expected Outcome: Cache contains mock issues, wiki pages, and comments; fixture identifiers are absent.
- Evidence Type: CLI command
- Freshness: Fresh temporary cache populated by current target runtime.
- Mock Policy: external_dependencies_only


## outcome-6
- Request Task: 6
- Role: `supporting_evidence`
- Request Item: `create-issue --live` reaches the mocked server, records audit/cache confirmation, and never returns `fixture client is read-only`.
- Target Surface: CLI command `gitcode-mcp create-issue --live`, audit, and cache
- Actor/Trigger: Operator creates an issue with live mode and a valid test token against the mock API.
- Expected Outcome: Create request reaches mock server; audit and cache confirmation are recorded; fixture read-only error is absent.
- Evidence Type: CLI command
- Freshness: Current target runtime with fresh temporary audit/cache state.
- Mock Policy: external_dependencies_only


## outcome-7
- Request Task: 7
- Role: `supporting_evidence`
- Request Item: Ordinary `sync` without `--live` remains fixture-backed and does not call the mock server.
- Target Surface: CLI command `gitcode-mcp sync`
- Actor/Trigger: Operator runs sync without `--live` while a mock server is available.
- Expected Outcome: Offline fixture-backed behavior is used and mock server request count remains zero.
- Evidence Type: CLI command
- Freshness: Current target test run with isolated server counter.
- Mock Policy: external_dependencies_only


## outcome-8
- Request Task: 8
- Role: `supporting_evidence`
- Request Item: Resolve and document one explicit API base URL rule for live commands.
- Target Surface: Live HTTP client base URL selection
- Actor/Trigger: Operator configures the selected API base URL authority and runs `gitcode-mcp sync --live`.
- Expected Outcome: Live client uses only the selected authority and request routing proves the rule.
- Evidence Type: CLI command
- Freshness: Current target tests with separate selected and non-selected mock endpoints.
- Mock Policy: external_dependencies_only


## outcome-9
- Request Task: 9
- Role: `supporting_evidence`
- Request Item: `doctor --live` reports effective provider mode, credential source, cache path, and API base URL used by the live client.
- Target Surface: CLI command `gitcode-mcp doctor --live --format json`
- Actor/Trigger: Operator runs doctor in live mode with mocked credentials and configured cache/API base URL.
- Expected Outcome: JSON reports effective live provider mode, credential source, cache path, and API base URL.
- Evidence Type: CLI command
- Freshness: Current target runtime and fresh temporary configuration.
- Mock Policy: explicit_allowed_mocks


## outcome-10
- Request Task: 10
- Role: `supporting_evidence`
- Request Item: `go test ./...` passes without real credentials, network, or OS Keychain access.
- Target Surface: Go test suite
- Actor/Trigger: Developer runs `go test ./...`.
- Expected Outcome: Offline CLI live integration tests pass and prove provider wiring without external dependencies.
- Evidence Type: local command
- Freshness: Current source tree and test suite.
- Mock Policy: external_dependencies_only
