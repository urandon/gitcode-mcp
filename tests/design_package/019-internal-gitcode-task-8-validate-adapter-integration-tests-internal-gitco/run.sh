#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."

OUTDIR="tests/design_package/019-internal-gitcode-task-8-validate-adapter-integration-tests-internal-gitco"
mkdir -p "$OUTDIR"

FAILED=0

echo "=== Scenario 1: go test ./internal/gitcode/... ==="
if go test -v -count=1 ./internal/gitcode/... 2>&1 | tee "$OUTDIR/gitcode_test_output.txt"; then
  echo "PASS: internal/gitcode/... tests pass"
else
  echo "FAIL: internal/gitcode/... tests failed"
  FAILED=1
fi

echo ""
echo "=== Scenario 2: All required test functions exist and pass ==="

REQUIRED_TESTS=(
  "TestBoundedWikiTreeTraversalCancelMidTraversal"
  "TestBoundedWikiTreeTraversalMaxRecords"
  "TestBoundedWikiTreeTraversalProgressEvents"
  "TestBoundedWikiTreeTraversalUnboundedBackwardCompat"
  "TestBoundedWikiTreeTraversalNoOuterLoopPattern"
  "TestEmptyWikiDetection400"
  "TestEmptyWikiDetection404"
  "TestEmptyWikiDetection400NonEmptyWiki"
  "TestEmptyWikiDetection404UninitializedMessage"
  "TestEmptyWikiDetectionEmptyArray200IsOK"
  "TestCreateWikiPageEmptyWikiDiagnostic"
  "TestScenario015WikiCreatePageFollowupConfirmation"
  "TestScenario015WikiCreatePageFollowupConfirmationFailure"
  "TestScenario016CreateIssueLabelsOmitted"
  "TestScenario016UpdateIssueTitleOnlyLabelsOmitted"
  "TestScenario016ExplicitLabelsPreserved"
  "TestScenario013003CreateIssueNoLabelsOmitsField"
  "TestScenario017AddCommentMalformedBodySchemaDecode"
  "TestScenario018PRListDetailCommentsRoutes"
  "TestScenario018PRCommentWrite"
)

PASS_LOG="tests/design_package/019-internal-gitcode-task-8-validate-adapter-integration-tests-internal-gitco/gitcode_test_output.txt"

for test_name in "${REQUIRED_TESTS[@]}"; do
  if grep -q "^--- PASS: ${test_name}\b" "$PASS_LOG" 2>/dev/null; then
    echo "  PASS: ${test_name}"
  else
    echo "  FAIL: ${test_name} not found or did not pass"
    FAILED=1
  fi
done

echo ""
echo "=== Scenario 2b: Service-level supporting tests ==="

if go test -v -count=1 ./internal/service/... 2>&1 | tee "$OUTDIR/service_test_output.txt"; then
  echo "PASS: internal/service/... tests pass"
else
  echo "FAIL: internal/service/... tests failed"
  FAILED=1
fi

SERVICE_TESTS=(
  "TestScenario017AddCommentLiveShapeCachesComment"
  "TestScenario017AddCommentMalformedBodyDiagnosticHTTPAttempted"
  "TestBulkSyncWikiBoundedCancelMidSync"
  "TestBulkSyncWikiBoundedSingleListWikiPagesCall"
  "TestBulkSyncWikiEmptyWikiDiagnosticUnbounded"
  "TestBulkSyncWikiEmptyWikiDiagnosticBounded"
  "TestNormalizeSyncFailureMapsEmptyWiki"
  "TestNormalizeWikiCachePath"
  "TestBulkSyncIssuesBoundedCancelMidway_CancelAfterPage3Progress"
)

SERVICE_LOG="$OUTDIR/service_test_output.txt"

for test_name in "${SERVICE_TESTS[@]}"; do
  if grep -q "^--- PASS: ${test_name}\b" "$SERVICE_LOG" 2>/dev/null; then
    echo "  PASS: ${test_name}"
  else
    echo "  FAIL: ${test_name} not found or did not pass"
    FAILED=1
  fi
done

echo ""
echo "=== Scenario 3: Git diff check ==="
if git diff --check; then
  echo "PASS: whitespace check clean"
else
  echo "FAIL: whitespace issues detected"
  FAILED=1
fi

if [ "$FAILED" -ne 0 ]; then
  echo ""
  echo "VALIDATION FAILED"
  exit 1
fi

echo ""
echo "VALIDATION PASSED"
exit 0
