# Validation Scenarios for 011-live-adapter-pr-comment-deferral-task-1-change-live-adapter-pr-comment-deferred-surface-ha

## Scenario: 011-live-adapter-pr-comment-deferral-task-1-change-live-adapter-pr-comment-deferred-surface-ha-scenario-1

**Trace:** User triggers production CLI/MCP PR and comment read/sync paths under mocked live-provider tests; `go test ./...` asserts the response is `unsupported_capability` with the configured diagnostic, no silent empty-cache result is returned, and no `live_transport_failure` is reported for the deferred surface.

**Verification:**

1. **SCEN-011A — Comment read (ListIssueComments) deferred:** `go test ./internal/gitcode/ -run TestScenario005RouteSchemaMatrixCommentsPreflightBlocksHTTP -count=1 -v` asserts the `ListIssueComments` method on `liveProvider` returns `ErrUnsupportedCapability` with `CapabilityKey == "comments_read"` and zero HTTP requests reach the mock server. The test server handler calls `t.Fatalf` on any HTTP request, proving no live HTTP is issued for the deferred comment read surface.

2. **SCEN-011B — Comment write (CreateIssueComment) deferred:** `go test ./internal/gitcode/ -run TestScenario011CreateIssueCommentPreflightBlocksHTTP -count=1 -v` asserts the `CreateIssueComment` method on `liveProvider` returns `ErrUnsupportedCapability` with `CapabilityKey == "comments_read"` and zero HTTP requests reach the mock server. The test server handler calls `t.Fatalf` on any HTTP request, proving no live HTTP is issued for the deferred comment write surface.

3. **SCEN-011C — PR read deferred at matrix level (Preflight):** `go test ./internal/gitcode/ -run "TestScenario005RouteSchemaMatrixPreflight/pull_requests_deferred" -count=1 -v` asserts `Preflight(ProductAreaPullRequests)` returns `ErrUnsupportedCapability` with `CapabilityKey == "pull_requests_read"`. The Provider interface has no PR methods, so no HTTP path exists for PRs — the matrix Preflight alone enforces the deferral contract.

4. **SCEN-011D — Supported surfaces still reach HTTP (Issues):** `go test ./internal/gitcode/ -run TestScenario005RouteSchemaMatrixIssuesReachesHTTP -count=1 -v` asserts that `ListIssues` on `liveProvider` successfully issues an HTTP request to the mock server and parses the response. This proves deferred-surface guards do not interfere with supported surfaces.

5. **SCEN-011E — Full offline `go test ./...` passes:** `go test ./... -count=1` passes across all packages without credentials, network, SSH agent, or OS Keychain. No test fails due to live network dependency, missing credentials, or external service unavailability.

6. **SCEN-011F — `git diff --check` passes:** `git diff --check` returns no whitespace warnings.

7. **SCEN-011G — No `live_transport_failure` on deferred surfaces:** The deferred-surface tests explicitly assert `ErrUnsupportedCapability` (not `ErrNetworkUnavailable`, which is the internal type for `live_transport_failure`). The mock server tripwire (`t.Fatalf` in the handler) proves no HTTP transport is ever attempted, so no transport failure can occur.

8. **SCEN-011H — Decommission-4 regression: schema_decode not classified as transport failure:** `go test ./internal/gitcode/ -run "TestIssueIdentity011SchemaDecodeNotTransport|TestLabel014SchemaDecodeDistinctFromTransport|TestLabel015ObjectLabelWithMissingIDReturnsSchemaDecode|TestIssueIdentity010MalformedPayloadSchemaDecodeDistinct|TestScenario002WikiMalformedEntrySchemaDecode|TestClassifierLiveDecommissionInvariant" -count=1 -v` passes, confirming that `ErrSchemaDecode` and `ErrAPIValidation` are distinct from `ErrNetworkUnavailable` (the internal type for `live_transport_failure`). Tests in `label_normalizer_test.go`, `issue_normalizer_test.go`, `client_test.go`, `error_classifier_test.go`, and `wiki_provider_test.go` explicitly verify schema decode errors do not match `ErrNetworkUnavailable`.

**Status:** PASS — `run.sh` executed successfully with 8/8 scenarios passing. All acceptance criteria met: comment read/write returns `ErrUnsupportedCapability` with configured diagnostic, PR deferral is enforced at matrix Preflight level, no silent empty-cache returned, no `live_transport_failure` reported for deferred surfaces, `go test ./...` and `git diff --check` both pass offline, and decommission-4 is verified.
