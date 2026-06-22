# Design Package Component: cli-startup

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: CLI Startup

## Summary
CLI startup needs one effective command-startup composition path that distinguishes explicit live mode from default fixture mode before constructing command dependencies. The current CLI parses `--live`, but ordinary command startup still constructs the default fixture-backed service without using live credential, repository binding, or selected base URL state.

## Top-Level Alignment
`cli-startup` owns provider-mode selection, startup diagnostics, and composition of repository binding, credential resolution, live provider, fixture provider, sync, write, and doctor surfaces. This design implements the approved narrow provider-wiring closure for explicit `--live` while preserving non-live fixture-backed sync.

## Tasks

### Task 1: StartupPlan selects live provider
Outcome IDs: outcome-1, outcome-2, outcome-7, outcome-8, outcome-9
Outcome Role: supporting_evidence
Decommission IDs: decommission-1
Change Type: change
Description: Introduce a CLI-owned startup plan that is built after option parsing and before service construction. The plan is the single component-local entity that decides `offline-fixture` versus `live-http`, resolves effective cache path, loads repository binding metadata, invokes the shared credential resolver for live commands, selects the authoritative repository `api_base_url`, and passes the resulting provider configuration to command services and doctor. Non-live `sync` continues to select the fixture provider and must not require credentials or live base URL configuration.
Existing Behavior / Reuse: Reuse existing option parsing, `--live` flag parsing, cache path resolution, config loading, credential provider interfaces, repository binding records, `service.NewWithMode`, write dispatch, sync dispatch, and doctor command dispatch. Confirmed absent in current CLI startup: the default service factory constructs `service.New(store)` unconditionally for normal commands, so parsed `opts.live` does not currently control provider construction through the real operator startup path; `doctor` is routed through local command handling and reports live readiness independently from the same startup composition snapshot used by sync/write.
Detailed Design: Add a component-local `StartupPlan` data structure owned by CLI startup with fields for command name, parsed options, effective provider mode, cache path, repo id, repository binding, selected API base URL, credential status metadata, secret token for provider construction only, service config, and a typed startup diagnostic. Build it with a `buildStartupPlan(ctx, command, opts, deps)` function called after `parseOptions` and before opening the store or dispatching normal commands; it must classify live-enabled commands as commands where `opts.live` is true for `sync`, write commands, and `doctor`, while all other command invocations remain `offline-fixture`.

For `offline-fixture`, `StartupPlan` resolves only the cache path and provider mode, opens the configured cache, and constructs the existing fixture service path. It must not call the credential resolver, must not construct a live provider config, and must not select or contact a live API base URL. The invariant is: if `opts.live` is false, startup plan provider mode is `offline-fixture`, service construction uses fixture provider semantics, and mock/live HTTP request count remains zero.

For `live-http`, `StartupPlan` loads effective configuration, opens the cache, reads the requested repository binding by `opts.repo`, and validates that the selected base URL is the binding `api_base_url` when present. If the repository binding has `api_base_url`, that value is the only value passed to the live provider config; environment or default base URLs are fallback-only when binding omits it and must be recorded as non-authoritative in the plan. Invalid or empty selected live base URLs produce a startup configuration diagnostic before service dispatch.

For credential handling, `StartupPlan` uses the shared `config.CredentialProvider` chain from CLI dependencies rather than reading `GITCODE_TOKEN` directly in command dispatch. If live mode has no usable credential, `buildStartupPlan` returns a typed `missing_credential` diagnostic before constructing the live service, before dispatching sync/write, and before any live HTTP request can be made. If a credential is present, the plan passes only the secret token value into `service.NewWithMode(... ProviderModeLive ...)` and exposes only non-secret source metadata for doctor output.

Change `executeWithFactoryAndDeps` so service construction receives the built `StartupPlan` instead of using `defaultServiceFactory(ctx, cachePath)` unconditionally. Keep the existing factory hook for tests by adapting it behind a startup-plan-aware factory or by making custom factory use an explicit `offline-fixture` plan. The plan must ensure `sync --live` and `create-issue --live` use `service.NewWithMode` with `ProviderModeLive`, selected base URL, timeout/retry limits, and credential-present state, while `sync` without `--live` continues to use the existing fixture service.

Change `doctor --live --format json` CLI handling to obtain its live fields from the same `StartupPlan` snapshot used by live command startup: effective provider mode must render as `live-http`, credential source must be non-secret source metadata, cache path must be the resolved path, and API base URL must be the selected authoritative base URL. Doctor may still use the doctor package for report assembly, but CLI startup must pass the plan’s effective values so doctor reports effective runtime composition rather than merely configured defaults.

For `decommission-1`, replace explicit live sync fallback to fixture-shaped results by making live plan construction fail closed: live sync cannot dispatch against a fixture service when credentials and base URL are usable. Keep the fixture provider internal for non-live commands only. Enforce the negative invariant by checking at startup that `opts.live` implies `ProviderModeLive` service construction or a typed startup failure; CLI integration tests must fail if live sync output/cache includes `ISSUE-42` or `WIKI-HOME`.
Acceptance Criteria: Operator runs `gitcode-mcp sync --live --repo <repo>` through the real CLI entrypoint with usable mocked credentials and repository binding `api_base_url`; the CLI command route constructs the live provider, the mock GitCode server receives authenticated requests, output/cache state contains mock records and no `ISSUE-42` or `WIKI-HOME`, proven by an offline Go CLI integration test. Operator runs `gitcode-mcp sync --live --repo <repo>` with no environment token and no mocked credential source; CLI startup returns a typed `missing_credential` diagnostic with failure status and the mock server request count remains zero, proven by an offline Go CLI integration test. Operator runs `gitcode-mcp sync --repo <repo>` without `--live` while a mock server is available; CLI startup selects `offline-fixture`, command completes through fixture-backed behavior, and mock request count remains zero, proven by an offline Go CLI integration test. Operator configures repository binding `api_base_url` to selected mock server and a non-authoritative alternative elsewhere, then runs `gitcode-mcp sync --live`; live HTTP requests hit only the selected server and never the non-selected endpoint, proven by request counters in the offline Go test. Operator runs `gitcode-mcp doctor --live --format json`; JSON reports effective provider mode `live-http`, non-secret credential source, resolved cache path, and selected API base URL from the same startup plan, proven by an offline CLI test using temporary paths and mocked credentials.
Workload: 1.5 MM

## Cross-Cutting Constraints
- Explicit live mode is fail-closed — live startup must either construct `live-http` with usable credential and valid selected base URL or return a typed startup diagnostic, never fixture fallback.
- Default non-live behavior remains cache-first and fixture-backed — ordinary `sync` must not resolve live credentials or contact live/mock HTTP endpoints.
- Credential material stays non-secret outside provider construction — startup may pass token value only to live provider setup and doctor may report source metadata only.
- Repository binding `api_base_url` is authoritative when present — CLI startup owns selecting that URL before live provider construction and before doctor reporting.
- Primary validation is offline — CLI startup acceptance must use mocked external GitCode API only and avoid real network, real credentials, and OS Keychain dependency.

## Data And Control Flow
- Parse command and flags — `executeWithFactoryAndDeps` and `parseOptions` — `--live` must be known before service construction.
- Build startup plan — `cli-startup` with config source, credential provider, cache path resolver, repository binding, and parsed options — provider mode is resolved exactly once per command invocation.
- Live credential gate — `StartupPlan` to shared credential resolver — missing credential returns before live provider construction or HTTP dispatch.
- Live base URL selection — repository binding to `StartupPlan` to `service.NewWithMode` — binding `api_base_url` wins over non-authoritative alternatives when present.
- Command dispatch — `StartupPlan` to sync/write/doctor dispatch — live commands receive live service configuration while non-live sync receives fixture service configuration.
- Doctor readiness — `StartupPlan` to doctor JSON response — reported provider mode, credential source, cache path, and API base URL are the effective startup values.

## Component Interactions
- `cli-startup` -> `credential-resolution` — invokes the shared credential resolver for live startup and receives secret token plus non-secret source metadata; no command-local token lookup is allowed for live provider construction.
- `cli-startup` -> `repository-binding` — reads repository identity, cache/audit binding, scopes, and authoritative `api_base_url` before live provider construction.
- `cli-startup` -> `live-provider` — supplies selected API base URL, token, repository identity, timeout/retry config, and live mode through `service.NewWithMode` or equivalent service construction.
- `cli-startup` -> `fixture-provider` — constructs fixture-backed service only when `--live` is absent, preserving default sync semantics.
- `cli-startup` -> `diagnostics` — maps startup failures such as `missing_credential`, invalid base URL, and configuration errors to stable CLI-visible diagnostics before dispatch.
- `cli-startup` -> `doctor-readiness` — passes the same effective startup snapshot used by live commands so doctor JSON reports effective rather than merely configured values.

## Rationale
This component is affected because the approved architecture assigns CLI startup ownership of effective provider-mode selection and command dependency composition. Existing source already has live-capable service construction and credential abstractions, but the real CLI command startup currently constructs the default fixture service before dispatch and does not use parsed `--live` to build the live provider path.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0226-run_attempt-1/final_message.txt`
