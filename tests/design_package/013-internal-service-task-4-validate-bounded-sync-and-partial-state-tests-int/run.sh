#!/usr/bin/env bash
set -euo pipefail

# Validation run script for task 013
# Validates bounded sync and partial state tests in internal/service

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$REPO_ROOT"

echo "=== Validating task 013: Bounded sync and partial state tests ==="
echo ""

PASSED=0
FAILED=0

report_pass() {
    echo "  PASS: $1"
    PASSED=$((PASSED + 1))
}

report_fail() {
    echo "  FAIL: $1"
    FAILED=$((FAILED + 1))
}

# ---------------------------------------------------------------------------
# Scenario 1: go test ./internal/service/... passes
# ---------------------------------------------------------------------------
echo "[Scenario 1] go test ./internal/service/... passes"
if go test ./internal/service/... -count=1 > /dev/null 2>&1; then
    report_pass "go test ./internal/service/... PASS"
else
    report_fail "go test ./internal/service/... FAIL"
fi

# ---------------------------------------------------------------------------
# Scenario 2: All bounded sync scenarios verified with mocked paginated providers
# ---------------------------------------------------------------------------
echo "[Scenario 2] All bounded sync scenarios verified with mocked paginated providers"

declare -a SCENARIO_TESTS=(
    "TestBulkSyncIssuesBoundedCancelMidway_CancelAfterPage3Progress"
    "TestBulkSyncIssuesBoundedCancelMidway"
    "TestBulkSyncIssuesBoundedTimeout"
    "TestBulkSyncIssuesBoundedProgressEvents"
    "TestBulkSyncIssuesBoundedMaxPages"
    "TestBulkSyncIssuesBoundedMaxRecords"
    "TestBulkSyncWikiBoundedPreCancel"
    "TestBulkSyncWikiBoundedMaxRecords"
    "TestBulkSyncWikiBoundedMaxPages"
    "TestBulkSyncWikiBoundedCancelMidSync"
    "TestBulkSyncWikiBoundedSingleListWikiPagesCall"
    "TestBulkSyncIssuesUnboundedBackwardCompat"
    "TestBulkSyncWikiUnboundedBackwardCompat"
    "TestBulkSyncAllBoundedAggregatesProgress"
    "TestBulkSyncWikiEmptyWikiDiagnosticUnbounded"
    "TestBulkSyncWikiEmptyWikiDiagnosticBounded"
)

for test_name in "${SCENARIO_TESTS[@]}"; do
    if go test ./internal/service/... -count=1 -run "^${test_name}$" > /dev/null 2>&1; then
        report_pass "$test_name PASS"
    else
        report_fail "$test_name FAIL"
    fi
done

# ---------------------------------------------------------------------------
# Scenario 3: PartialSyncError fields inspected for success_count, diagnostic class
# ---------------------------------------------------------------------------
echo "[Scenario 3] PartialSyncError fields inspected for success_count, diagnostic class"

# Verify the test file contains the required field assertions
TEST_FILE="$REPO_ROOT/internal/service/sync_bounds_test.go"

# Check for success_count assertions on PartialSyncError
if grep -q 'partial\.SuccessCount' "$TEST_FILE"; then
    report_pass "PartialSyncError.SuccessCount assertions present"
else
    report_fail "PartialSyncError.SuccessCount assertions missing"
fi

# Check for diagnostic assertions
if grep -q 'partial\.Diagnostic' "$TEST_FILE"; then
    report_pass "PartialSyncError.Diagnostic assertions present"
else
    report_fail "PartialSyncError.Diagnostic assertions missing"
fi

# Check specific diagnostic values are tested
# The test file uses SyncDiagnostic constants defined in sync_bounds.go
# whose values are "sync_cancelled", "sync_timeout", "empty_wiki"
CANCEL_COUNT=$(grep -c 'SyncDiagnosticCancelled' "$TEST_FILE" || true)
TIMEOUT_COUNT=$(grep -c 'SyncDiagnosticTimeout' "$TEST_FILE" || true)
EMPTY_COUNT=$(grep -c 'SyncDiagnosticEmptyWiki' "$TEST_FILE" || true)

if [ "$CANCEL_COUNT" -gt 0 ]; then
    report_pass "Diagnostic class 'sync_cancelled' tested (SyncDiagnosticCancelled: $CANCEL_COUNT refs)"
else
    report_fail "Diagnostic class 'sync_cancelled' NOT tested"
fi

if [ "$TIMEOUT_COUNT" -gt 0 ]; then
    report_pass "Diagnostic class 'sync_timeout' tested (SyncDiagnosticTimeout: $TIMEOUT_COUNT refs)"
else
    report_fail "Diagnostic class 'sync_timeout' NOT tested"
fi

if [ "$EMPTY_COUNT" -gt 0 ]; then
    report_pass "Diagnostic class 'empty_wiki' tested (SyncDiagnosticEmptyWiki: $EMPTY_COUNT refs)"
else
    report_fail "Diagnostic class 'empty_wiki' NOT tested"
fi

# Check TotalRequested field inspected
if grep -q 'partial\.TotalRequested' "$TEST_FILE"; then
    report_pass "PartialSyncError.TotalRequested assertions present"
else
    report_fail "PartialSyncError.TotalRequested assertions missing"
fi

# ---------------------------------------------------------------------------
# Scenario 4: Progress channel events counted and verified
# ---------------------------------------------------------------------------
echo "[Scenario 4] Progress channel events counted and verified"

if go test ./internal/service/... -count=1 -run "TestBulkSyncIssuesBoundedProgressEvents" > /dev/null 2>&1; then
    report_pass "TestBulkSyncIssuesBoundedProgressEvents PASS"
else
    report_fail "TestBulkSyncIssuesBoundedProgressEvents FAIL"
fi

if go test ./internal/service/... -count=1 -run "TestProgressEventNonBlockingSend" > /dev/null 2>&1; then
    report_pass "TestProgressEventNonBlockingSend PASS"
else
    report_fail "TestProgressEventNonBlockingSend FAIL"
fi

# Verify events asserted with Collection and RecordsFetched fields
if grep -q 'ev\.Collection' "$TEST_FILE" && grep -q 'ev\.RecordsFetched' "$TEST_FILE"; then
    report_pass "Progress event fields (Collection, RecordsFetched) verified"
else
    report_fail "Progress event fields NOT verified"
fi

# ---------------------------------------------------------------------------
# Scenario 5: Wiki tree walker cancellation verified mid-level
# ---------------------------------------------------------------------------
echo "[Scenario 5] Wiki tree walker cancellation verified mid-level"

if go test ./internal/service/... -count=1 -run "TestBulkSyncWikiBoundedCancelMidSync" > /dev/null 2>&1; then
    report_pass "TestBulkSyncWikiBoundedCancelMidSync PASS"
else
    report_fail "TestBulkSyncWikiBoundedCancelMidSync FAIL"
fi

if go test ./internal/service/... -count=1 -run "TestBulkSyncWikiBoundedPreCancel" > /dev/null 2>&1; then
    report_pass "TestBulkSyncWikiBoundedPreCancel PASS"
else
    report_fail "TestBulkSyncWikiBoundedPreCancel FAIL"
fi

# Decommission-3: single ListWikiPages call
if go test ./internal/service/... -count=1 -run "TestBulkSyncWikiBoundedSingleListWikiPagesCall" > /dev/null 2>&1; then
    report_pass "TestBulkSyncWikiBoundedSingleListWikiPagesCall (decommission-3) PASS"
else
    report_fail "TestBulkSyncWikiBoundedSingleListWikiPagesCall (decommission-3) FAIL"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=== Results ==="
echo "PASSED: $PASSED"
echo "FAILED: $FAILED"

if [ "$FAILED" -gt 0 ]; then
    echo "VALIDATION FAILED"
    exit 1
else
    echo "VALIDATION PASSED"
    exit 0
fi
