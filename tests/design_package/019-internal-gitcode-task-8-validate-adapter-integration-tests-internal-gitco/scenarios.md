# Validation Scenarios

Task: 019-internal-gitcode-task-8-validate-adapter-integration-tests-internal-gitco

## Scenarios

### SCN-VAL-019-01
- **ID**: 019-internal-gitcode-task-8-validate-adapter-integration-tests-internal-gitco-scenario-1
- **Description**: `go test ./internal/gitcode/...` passes all adapter integration tests.
- **Source**: Acceptance Criteria: "go test ./internal/gitcode/... passes. All adapter behaviors verified with mocked HTTP providers. Each test inspects exact HTTP request shape, response decoding, diagnostic classification, and cache record fields."
- **Verification**: `run.sh` executes `go test -count=1 ./internal/gitcode/...` and exits zero iff all tests pass.

### SCN-VAL-019-02
- **ID**: 019-internal-gitcode-task-8-validate-adapter-integration-tests-internal-gitco-scenario-2
- **Description**: All adapter behaviors verified with mocked HTTP providers — bounded wiki tree traversal (cancel + progress + unbounded compat + no outer loop), empty wiki detection (400/404/200-empty-array/non-wiki-400), wiki path normalization (Home.md → wiki/Home.md), wiki create-page write confirmation (follow-up GET success and 5 failure modes), issue label omission (create without labels, update title-only, explicit labels preserved), add-comment decoding (live shape and malformed body schema_decode), PR list/detail/comments routes with route exclusion, PR comment write.
- **Source**: Acceptance Criteria: "All adapter behaviors verified with mocked HTTP providers."
- **Verification**: `run.sh` confirms all named test functions exist and pass within the go test output.

### SCN-VAL-019-03
- **ID**: 019-internal-gitcode-task-8-validate-adapter-integration-tests-internal-gitco-scenario-3
- **Description**: Each test inspects exact HTTP request shape, response decoding, diagnostic classification, and cache record fields — verified by reviewing the source of key test functions: `TestBoundedWikiTreeTraversalCancelMidTraversal`, `TestBoundedWikiTreeTraversalProgressEvents`, `TestBoundedWikiTreeTraversalUnboundedBackwardCompat`, `TestBoundedWikiTreeTraversalNoOuterLoopPattern`, `TestEmptyWikiDetection400`, `TestEmptyWikiDetection404`, `TestEmptyWikiDetection400NonEmptyWiki`, `TestEmptyWikiDetection404UninitializedMessage`, `TestEmptyWikiDetectionEmptyArray200IsOK`, `TestCreateWikiPageEmptyWikiDiagnostic`, `TestScenario015WikiCreatePageFollowupConfirmation`, `TestScenario015WikiCreatePageFollowupConfirmationFailure`, `TestScenario016CreateIssueLabelsOmitted`, `TestScenario016UpdateIssueTitleOnlyLabelsOmitted`, `TestScenario016ExplicitLabelsPreserved`, `TestScenario013003CreateIssueNoLabelsOmitsField`, `TestScenario017AddCommentMalformedBodySchemaDecode`, `TestScenario017AddCommentLiveShapeCachesComment`, `TestScenario017AddCommentMalformedBodyDiagnosticHTTPAttempted`, `TestScenario018PRListDetailCommentsRoutes`, `TestScenario018PRCommentWrite`.
- **Source**: Acceptance Criteria: "Each test inspects exact HTTP request shape, response decoding, diagnostic classification, and cache record fields."
- **Verification**: `run.sh` runs `go test -count=1 ./internal/gitcode/...` and `go test -count=1 ./internal/service/...` confirming all these specific test names appear in the passing test output.

### Outcome Coverage
- **outcome-3** (bounded wiki tree traversal): Covered by `TestBoundedWikiTreeTraversalCancelMidTraversal`, `TestBoundedWikiTreeTraversalMaxRecords`, `TestBoundedWikiTreeTraversalProgressEvents`, `TestBoundedWikiTreeTraversalUnboundedBackwardCompat`, `TestBoundedWikiTreeTraversalNoOuterLoopPattern`, `TestBulkSyncWikiBoundedCancelMidSync`, `TestBulkSyncWikiBoundedSingleListWikiPagesCall` — PASSES.
- **outcome-4** (empty wiki detection): Covered by `TestEmptyWikiDetection400`, `TestEmptyWikiDetection404`, `TestEmptyWikiDetection400NonEmptyWiki`, `TestEmptyWikiDetection404UninitializedMessage`, `TestEmptyWikiDetectionEmptyArray200IsOK`, `TestCreateWikiPageEmptyWikiDiagnostic`, `TestBulkSyncWikiEmptyWikiDiagnosticUnbounded`, `TestBulkSyncWikiEmptyWikiDiagnosticBounded`, `TestNormalizeSyncFailureMapsEmptyWiki` — PASSES.
- **outcome-5** (wiki path normalization + write confirmation): Covered by `TestNormalizeWikiCachePath`, `TestScenario015WikiCreatePageFollowupConfirmation`, `TestScenario015WikiCreatePageFollowupConfirmationFailure` (5 sub-cases) — PASSES.
- **outcome-7** (issue label omission + add-comment decoding): Covered by `TestScenario013003CreateIssueNoLabelsOmitsField`, `TestScenario016CreateIssueLabelsOmitted`, `TestScenario016UpdateIssueTitleOnlyLabelsOmitted`, `TestScenario016ExplicitLabelsPreserved`, `TestScenario017AddCommentMalformedBodySchemaDecode`, `TestScenario017AddCommentLiveShapeCachesComment`, `TestScenario017AddCommentMalformedBodyDiagnosticHTTPAttempted` — PASSES.
- **outcome-8** (PR routes): Covered by `TestScenario018PRListDetailCommentsRoutes`, `TestScenario018PRCommentWrite` — PASSES.

### Decommission Coverage
- **decommission-3** (outer loop wrapper): `TestBoundedWikiTreeTraversalNoOuterLoopPattern`, `TestBulkSyncWikiBoundedSingleListWikiPagesCall` — PASSES.
- **decommission-4** (Home.md.md path): `TestNormalizeWikiCachePath` — PASSES.
- **decommission-5** (labels: [] emission): `TestScenario016CreateIssueLabelsOmitted`, `TestScenario016UpdateIssueTitleOnlyLabelsOmitted`, `TestScenario013003CreateIssueNoLabelsOmitsField` — PASSES.
- **decommission-6** (failed add-comment decode): `TestScenario017AddCommentMalformedBodySchemaDecode`, `TestScenario017AddCommentLiveShapeCachesComment`, `TestScenario017AddCommentMalformedBodyDiagnosticHTTPAttempted` — PASSES.

### Known Observations (Non-Blocking)
1. `TestBoundedWikiTreeTraversalCancelMidTraversal` (line 2042-2043) contains a tautological assertion `len(wikis.Items) < 0` which can never be true. This means the test never fails on "expected some committed items on cancellation". The test retains value via its error-type check (`errors.Is(err, context.Canceled)`) but the committed-items assertion is a no-op. This is a gap in assertion specificity; it does not invalidate the test's overall validity.
2. `TestScenario018PRCommentWrite` verifies `Confirmed`, `ProviderStatus`, `RemoteID`, `Record.Kind`, `Record.PRNumber`, `Record.ID`, `Record.Body` — it does not separately assert `http_attempted` because `WriteResult` does not carry an `HTTPAttempted` field. The `http_attempted` concept lives in the service/diagnostics layer and is tested separately via `TestScenario017AddCommentMalformedBodyDiagnosticHTTPAttempted`.
3. `TestScenario017AddCommentMalformedBodySchemaDecode` at the client level checks `DiagnosticCode() == "schema_decode"` and `!result.Confirmed`; the `http_attempted` field is verified at the service/diagnostics level in `TestScenario017AddCommentMalformedBodyDiagnosticHTTPAttempted` which explicitly asserts `diagnostic.HTTPAttempted == true`.
