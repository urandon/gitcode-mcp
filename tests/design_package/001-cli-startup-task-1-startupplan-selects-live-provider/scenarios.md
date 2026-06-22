# Scenarios: StartupPlan selects live provider

## 001-cli-startup-task-1-startupplan-selects-live-provider-scenario-1

Operator runs `gitcode-mcp sync --live --repo <repo>` through the real CLI entrypoint with usable mocked credentials and repository binding `api_base_url`; the CLI command route constructs the live provider, the mock GitCode server receives authenticated requests, output/cache state contains mock records and no `ISSUE-42` or `WIKI-HOME`, proven by an offline Go CLI integration test.

Concrete executable coverage: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER' -count=1`.

Expected product failure if broken: the mock server request counter stays zero, the command fails, or fixture identifiers appear in the command output.

## 001-cli-startup-task-1-startupplan-selects-live-provider-scenario-2

Operator runs `gitcode-mcp sync --live --repo <repo>` with no environment token and no mocked credential source; CLI startup returns a typed `missing_credential` diagnostic with failure status and the mock server request count remains zero, proven by an offline Go CLI integration test.

Concrete executable coverage: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-MISSING-CREDENTIAL' -count=1`.

Expected product failure if broken: the command succeeds, the diagnostic is not typed as `missing_credential`, or the mock server observes any HTTP request.

## 001-cli-startup-task-1-startupplan-selects-live-provider-scenario-3

Operator runs `gitcode-mcp sync --repo <repo>` without `--live` while a mock server is available; CLI startup selects `offline-fixture`, command completes through fixture-backed behavior, and mock request count remains zero, proven by an offline Go CLI integration test.

Concrete executable coverage: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-OFFLINE-SYNC-NO-HTTP' -count=1`.

Expected product failure if broken: non-live sync requires live credentials, fails live startup checks, or contacts the mock HTTP server.

## 001-cli-startup-task-1-startupplan-selects-live-provider-scenario-4

Operator configures repository binding `api_base_url` to selected mock server and a non-authoritative alternative elsewhere, then runs `gitcode-mcp sync --live`; live HTTP requests hit only the selected server and never the non-selected endpoint, proven by request counters in the offline Go test.

Concrete executable coverage: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-API-BASE-AUTHORITY' -count=1`.

Expected product failure if broken: the selected mock server receives no requests, or the alternate non-authoritative server receives any request.

## 001-cli-startup-task-1-startupplan-selects-live-provider-scenario-5

Operator runs `gitcode-mcp doctor --live --format json`; JSON reports effective provider mode `live-http`, non-secret credential source, resolved cache path, and selected API base URL from the same startup plan, proven by an offline CLI test using temporary paths and mocked credentials.

Concrete executable coverage: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-DOCTOR-LIVE-JSON-STARTUP-SNAPSHOT' -count=1`.

Expected product failure if broken: doctor reports configured/default values instead of effective startup-plan values, leaks the token, contacts the mock server, or omits provider mode, credential source, cache path, or selected API base URL.
