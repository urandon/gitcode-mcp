#!/usr/bin/env bash
set -euo pipefail

# Materialize validation for task 011-live-adapter-pr-comment-deferral-task-1-change-live-adapter-pr-comment-deferred-surface-ha.
# Validates that PR and comment read/sync paths return unsupported_capability diagnostics,
# no silent empty-cache result is returned, and no live_transport_failure is reported.
# Must pass without credentials, network, SSH agent, or OS Keychain.

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

PASS_COUNT=0
FAIL_COUNT=0

pass() {
  echo "PASS: $1"
  PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
  echo "FAIL: $1"
  FAIL_COUNT=$((FAIL_COUNT + 1))
}

echo "=== SCEN-011A: Comment read (ListIssueComments) deferred ==="
if go test "${REPO_ROOT}/internal/gitcode/" -run TestScenario005RouteSchemaMatrixCommentsPreflightBlocksHTTP -count=1 -v 2>&1; then
  pass "ListIssueComments returns ErrUnsupportedCapability with comments_read; zero HTTP requests"
else
  fail "ListIssueComments test failed"
fi

echo ""
echo "=== SCEN-011B: Comment write (CreateIssueComment) deferred ==="
if go test "${REPO_ROOT}/internal/gitcode/" -run TestScenario011CreateIssueCommentPreflightBlocksHTTP -count=1 -v 2>&1; then
  pass "CreateIssueComment returns ErrUnsupportedCapability with comments_read; zero HTTP requests"
else
  fail "CreateIssueComment test failed"
fi

echo ""
echo "=== SCEN-011C: PR read deferred at matrix level (Preflight) ==="
if go test "${REPO_ROOT}/internal/gitcode/" -run "TestScenario005RouteSchemaMatrixPreflight/pull_requests_deferred" -count=1 -v 2>&1; then
  pass "Preflight(ProductAreaPullRequests) returns ErrUnsupportedCapability with pull_requests_read"
else
  fail "Preflight(ProductAreaPullRequests) test failed"
fi

echo ""
echo "=== SCEN-011D: Supported surfaces still reach HTTP ==="
if go test "${REPO_ROOT}/internal/gitcode/" -run TestScenario005RouteSchemaMatrixIssuesReachesHTTP -count=1 -v 2>&1; then
  pass "ListIssues reaches HTTP and parses response successfully"
else
  fail "ListIssues (supported surface) test failed"
fi

echo ""
echo "=== SCEN-011E: Full offline go test ./... ==="
if go test "${REPO_ROOT}/..." -count=1 -v 2>&1; then
  pass "go test ./... passes for all packages offline"
else
  fail "go test ./... failed for one or more packages"
fi

echo ""
echo "=== SCEN-011F: git diff --check ==="
if git -C "${REPO_ROOT}" diff --check 2>&1; then
  pass "git diff --check passes with no whitespace violations"
else
  fail "git diff --check reported whitespace issues"
fi

echo ""
echo "=== SCEN-011G: No live_transport_failure on deferred surfaces ==="
# Run the route schema matrix tests that collectively prove no live_transport_failure:
# - Comments preflight blocks HTTP (no transport attempted, so no transport failure possible)
# - CreateIssueComment preflight blocks HTTP (same)
# - Preflight for pull_requests returns unsupported_capability (no transport)
if go test "${REPO_ROOT}/internal/gitcode/" \
  -run "TestScenario005RouteSchemaMatrixCommentsPreflightBlocksHTTP|TestScenario011CreateIssueCommentPreflightBlocksHTTP|TestScenario005RouteSchemaMatrixPreflight/pull_requests" \
  -count=1 -v 2>&1; then
  pass "Deferred-surface tests collectively prove ErrUnsupportedCapability, not ErrNetworkUnavailable"
else
  fail "Deferred-surface transport-failure test failed"
fi

echo ""
echo "=== SCEN-011H: Decommission-4 — schema_decode not classified as transport failure ==="
# Verify ErrSchemaDecode does not match ErrNetworkUnavailable.
# The label_normalizer_test.go and issue_normalizer_test.go have explicit assertions
# that schema decode errors do not match ErrNetworkUnavailable.
if go test "${REPO_ROOT}/internal/gitcode/" \
  -run "TestIssueIdentity011SchemaDecodeNotTransport|TestLabel014SchemaDecodeDistinctFromTransport|TestLabel015ObjectLabelWithMissingIDReturnsSchemaDecode|TestIssueIdentity010MalformedPayloadSchemaDecodeDistinct|TestScenario002WikiMalformedEntrySchemaDecode|TestClassifierLiveDecommissionInvariant" \
  -count=1 -v 2>&1; then
  pass "Schema decode errors are not classified as network errors; 400/schema/decode distinct from transport"
else
  fail "Decommission-4 regression test failed"
fi

echo ""
echo "============================================"
echo "RESULTS: ${PASS_COUNT} passed, ${FAIL_COUNT} failed"
echo "============================================"

if [ "${FAIL_COUNT}" -gt 0 ]; then
  exit 1
fi
exit 0
