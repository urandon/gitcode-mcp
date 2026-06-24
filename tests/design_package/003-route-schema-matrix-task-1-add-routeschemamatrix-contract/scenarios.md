# Scenarios: Add RouteSchemaMatrix contract

## 003-route-schema-matrix-task-1-add-routeschemamatrix-contract-scenario-1

Developer runs `go test ./...` without credentials, network, SSH agent, or OS Keychain; adapter-level tests construct the production live provider with `DefaultRouteSchemaMatrix()` and trigger issue, label, milestone, PR, comment, and wiki product surfaces against `httptest` GitCode routes.

Executable validation:

- Run `go test ./internal/gitcode -run "^TestScenario005" -count=1` and verify all matrix scenario tests pass, exercising `DefaultRouteSchemaMatrix()` construction, `Spec` lookup, `RequireDeclared`, `ValidateCoverage`, and `Preflight` through production `RouteSchemaMatrix` code paths.
- Run `go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixLiveProviderConstruction$" -count=1` to assert the production `NewLiveProvider` with default matrix validates all six product areas (issues, labels, milestones, wiki, pull_requests, comments) and succeeding construction.
- Run `go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixIssuesReachesHTTP$" -count=1` to assert issues supported surface reaches the mocked `/api/v5/repos/{owner}/{repo}/issues` route through `p.ListIssues` on a matrix-equipped live provider.
- Run `go test ./internal/diagnostics -run "^TestClassifierUnsupportedCapability$" -count=1` to assert the production classifier maps `"unsupported_capability"` diagnostic code to `CodeUnsupportedCapability` with exit class `"capability"`, `HTTPAttempted=false`, and `Retryable=false`.

Expected result: All focused tests pass; no network, credentials, SSH agent, or OS Keychain access is required.

## 003-route-schema-matrix-task-1-add-routeschemamatrix-contract-scenario-2

Issue, label, milestone, and wiki supported surfaces continue to the appropriate mocked `/api/v5` route family and either parse GitCode-shaped responses or hand off to their normal parser, while PR/comment read attempts return visible `unsupported_capability` diagnostics with capability keys `pull_requests_read` and `comments_read`, no empty success result, no outbound HTTP request, and no `live_transport_failure`.

Executable validation:

- Run `go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixCommentsPreflightBlocksHTTP$" -count=1` to verify that `p.ListIssueComments` on a matrix-equipped live provider returns `ErrUnsupportedCapability` with `CapabilityKey == "comments_read"` before any HTTP request is made (verified by `httptest.Server` handler that fails on any unexpected request).
- Run `go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixPreflight$" -count=1` to verify `Preflight("pull_requests")` returns `ErrUnsupportedCapability` with `CapabilityKey == "pull_requests_read"` and `Preflight("issues")` returns nil.
- Run `go test ./internal/gitcode -run "^TestScenario005ErrUnsupportedCapabilityDiagnosticCode$" -count=1` to verify `ErrUnsupportedCapability.DiagnosticCode()` returns `"unsupported_capability"`.
- Run `go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixIssuesReachesHTTP$" -count=1` to verify the supported issues path reaches `httptest.Server` successfully, confirming that only deferred surfaces are blocked.

Expected result: PR and comment reads produce typed `ErrUnsupportedCapability` errors with correct capability keys, no HTTP round-trip, and no silent empty success. Supported issue paths complete successfully through mocked HTTP.

## 003-route-schema-matrix-task-1-add-routeschemamatrix-contract-scenario-3

Matrix validation tests mutate the default matrix to remove a required product area, use an unknown enum value, use a non-`/api/v5` route family, mark a supported spec with `deferred` evidence, and mark a deferred spec without `unsupported_capability`; each case fails provider construction or preflight deterministically before any network call.

Executable validation:

- Run `go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixValidateCoverageMissingArea$" -count=1` to verify that an empty matrix fails `RequireDeclared` for a known product area.
- Run `go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixValidateCoverageBadEnums$" -count=1` to verify that unknown `SupportStatus`, unknown `EvidenceClass`, non-`/api/v5` route family, and empty route family each fail validation.
- Run `go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixValidateCoverageContradictorySpec$" -count=1` to verify that supported spec with deferred evidence, deferred spec without `unsupported_capability` code, deferred spec without diagnostic, deferred spec without capability key, deferred spec without message, and supported spec with diagnostic each fail validation.
- Run `go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixLiveProviderConstruction$" -count=1` to verify that `NewLiveProvider` with a matrix missing a required area or with a bad enum value fails construction before any network call.

Expected result: All validation-rejection cases fail deterministically at provider construction or preflight time, with no outbound HTTP and no `live_transport_failure`.
