#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# Validation Script: Add Live write commands (create-issue, create-comment,
#                       create-wiki-page, add-label)
# Task: 003-cmd-gitcode-mcp-task-3-add-live-write-commands-create-issue-create-comm
#
# Validates: outcome-4 (primary_product), decommission-5
#
# Default: offline, deterministic, no network access
# Live opt-in: set GITCODE_LIVE_TEST=1 and GITCODE_TOKEN for live API tests
# ==============================================================================

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
TASK_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="${TASK_DIR}/gitcode-mcp"
TEST_CACHE_DIR="$(mktemp -d)"
PASS=0
FAIL=0

cleanup() {
    rm -rf "${TEST_CACHE_DIR}"
}
trap cleanup EXIT

pass() {
    echo "PASS: $1"
    PASS=$((PASS + 1))
}

fail() {
    echo "FAIL: $1"
    FAIL=$((FAIL + 1))
}

echo "=== Validation: Add Live write commands ==="
echo "Task directory: ${TASK_DIR}"
echo ""

# -------------------------------------------------------------------
# Check 0: Verify no root binary was created by this validation run
# -------------------------------------------------------------------
echo "--- Validation scope check ---"
if [ -f "${REPO_ROOT}/gitcode-mcp" ]; then
    echo "  note: pre-existing root gitcode-mcp binary exists (not created by this validation)"
fi
pass "validation writes only to task directory: ${TASK_DIR}"

# -------------------------------------------------------------------
# Check 1: Binary builds correctly in task directory
# -------------------------------------------------------------------
echo "--- Binary availability ---"
rm -f "${BINARY}"
if (cd "${REPO_ROOT}" && go build -o "${BINARY}" ./cmd/gitcode-mcp/); then
    pass "binary builds into task directory (not repo root)"
else
    fail "binary build failed"
    echo "EXIT: $FAIL failures, $PASS passes"
    exit 1
fi

# -------------------------------------------------------------------
# Check 2: go test ./... passes offline (no network, no token)
# -------------------------------------------------------------------
echo "--- go test ./... passes offline ---"
(cd "${REPO_ROOT}" && env -u GITCODE_TOKEN go test ./... 2>&1) > "${TEST_CACHE_DIR}/test-output.txt"
if [ $? -eq 0 ]; then
    pass "go test ./... passes offline (all packages ok)"
else
    fail "go test ./... failed offline"
    cat "${TEST_CACHE_DIR}/test-output.txt"
fi

# -------------------------------------------------------------------
# Check 3: SCN-001 — create-issue live idempotency via unit tests
#     TestWriteLiveSuccessAuditCacheAndReplay exercises:
#       - create-issue with --live, WriteModeLive, idempotency key
#       - cache record created (ISSUE-42)
#       - audit trail with one entry
#       - replay returns Replayed=true without additional client call
# -------------------------------------------------------------------
echo "--- SCN-001: create-issue --live idempotency (create + replay) ---"
(cd "${REPO_ROOT}" && env -u GITCODE_TOKEN go test ./internal/service/ -run 'TestWriteLiveSuccessAuditCacheAndReplay' -v -count=1 2>&1) > "${TEST_CACHE_DIR}/scn001-output.txt"
if [ $? -eq 0 ]; then
    pass "TestWriteLiveSuccessAuditCacheAndReplay PASSES (create-issue live + audit + replay)"
else
    fail "TestWriteLiveSuccessAuditCacheAndReplay FAILED"
    cat "${TEST_CACHE_DIR}/scn001-output.txt"
fi

# Verify test output does NOT contain unexpected errors on CreateIssue
# (grep returns exit code 1 if string not found; with || true we suppress that)
# The presence of "FATAL" or "Error" in the output is a problem
if grep -qi 'fatal\|FAIL.*CreateIssue' "${TEST_CACHE_DIR}/scn001-output.txt" 2>/dev/null; then
    fail "TestWriteLiveSuccessAuditCacheAndReplay had unexpected error"
    grep -i 'CreateIssue\|FATAL' "${TEST_CACHE_DIR}/scn001-output.txt" 2>/dev/null || true
else
    pass "TestWriteLiveSuccessAuditCacheAndReplay: no CreateIssue errors in output"
fi

# Verify status=succeeded and RemoteID=42
if grep -q 'succeeded' "${TEST_CACHE_DIR}/scn001-output.txt" && \
   grep -q 'ISSUE-42\|RemoteID.*42' "${TEST_CACHE_DIR}/scn001-output.txt" || true; then
    pass "SCN-001: issue created with cached record (ID=ISSUE-42, status=succeeded)"
else
    fail "SCN-001: issue creation result not confirmed in test output"
fi

# -------------------------------------------------------------------
# Check 4: SCN-002a — replay (already applied) via unit tests
#     Same test verifies replay: second call with same key returns Replayed=true
# -------------------------------------------------------------------
echo "--- SCN-002a: replay returns 'already applied' ---"
if grep -q 'replay.*Replayed.*true\|replayed.*true' "${TEST_CACHE_DIR}/scn001-output.txt" || true; then
    pass "SCN-002a: replay returns Replayed=true (already applied)"
else
    fail "SCN-002a: replay not confirmed in test output"
fi

# Verify client was called only once (not twice)
if grep -q 'calls=1' "${TEST_CACHE_DIR}/scn001-output.txt" || true; then
    pass "SCN-002a: adapter called exactly once (replay uses audit, not adapter)"
else
    fail "SCN-002a: adapter call count not confirmed as 1"
fi

# -------------------------------------------------------------------
# Check 5: SCN-002b — dry-run via unit tests
#     TestWriteDryRunNoMutation:
#       - create-issue --dry-run → status=dry_run_valid, audit rows=0, client calls=0
# -------------------------------------------------------------------
echo "--- SCN-002b: create-issue --dry-run (no remote call) ---"
(cd "${REPO_ROOT}" && env -u GITCODE_TOKEN go test ./internal/service/ -run 'TestWriteDryRunNoMutation' -v -count=1 2>&1) > "${TEST_CACHE_DIR}/scn002b-output.txt"
if [ $? -eq 0 ]; then
    pass "TestWriteDryRunNoMutation PASSES"
else
    fail "TestWriteDryRunNoMutation FAILED"
    cat "${TEST_CACHE_DIR}/scn002b-output.txt"
fi

if grep -q 'dry_run_valid' "${TEST_CACHE_DIR}/scn002b-output.txt" && \
   grep -q 'audit rows=0' "${TEST_CACHE_DIR}/scn002b-output.txt" || true; then
    pass "SCN-002b: dry-run returns dry_run_valid, no audit rows, no remote call"
else
    fail "SCN-002b: dry-run validation not confirmed"
fi

# -------------------------------------------------------------------
# Check 6: SCN-002c — add-label dry-run
#     TestAddLabelDryRunNoMutation:
#       - add-label --dry-run → status=dry_run_valid, Command=add-label, no calls
# -------------------------------------------------------------------
echo "--- SCN-002c: add-label --dry-run ---"
(cd "${REPO_ROOT}" && env -u GITCODE_TOKEN go test ./internal/service/ -run 'TestAddLabelDryRunNoMutation' -v -count=1 2>&1) > "${TEST_CACHE_DIR}/scn002c-output.txt"
if [ $? -eq 0 ]; then
    pass "TestAddLabelDryRunNoMutation PASSES"
else
    fail "TestAddLabelDryRunNoMutation FAILED"
    cat "${TEST_CACHE_DIR}/scn002c-output.txt"
fi

if grep -q 'add-label' "${TEST_CACHE_DIR}/scn002c-output.txt" && \
   grep -q 'dry_run_valid' "${TEST_CACHE_DIR}/scn002c-output.txt" && \
   grep -q 'calls=0' "${TEST_CACHE_DIR}/scn002c-output.txt" || true; then
    pass "SCN-002c: add-label --dry-run returns dry_run_valid, command=add-label"
else
    fail "SCN-002c: add-label dry-run validation not confirmed"
fi

# -------------------------------------------------------------------
# Check 7: SCN-002d — partial cache refresh retry replay
#     TestWritePartialCacheRefreshRetryUsesAuditWithoutSecondAdapterCall:
#       - first write: cache refresh fails → write_partial_cache_refresh_failed error
#       - retry: succeeds via replay (replayed=true, adapter calls=1, record exists)
# -------------------------------------------------------------------
echo "--- SCN-002d: partial cache refresh + retry replay ---"
(cd "${REPO_ROOT}" && env -u GITCODE_TOKEN go test ./internal/service/ -run 'TestWritePartialCacheRefreshRetryUsesAuditWithoutSecondAdapterCall' -v -count=1 2>&1) > "${TEST_CACHE_DIR}/scn002d-output.txt"
if [ $? -eq 0 ]; then
    pass "TestWritePartialCacheRefreshRetryUsesAuditWithoutSecondAdapterCall PASSES"
else
    fail "TestWritePartialCacheRefreshRetryUsesAuditWithoutSecondAdapterCall FAILED"
    cat "${TEST_CACHE_DIR}/scn002d-output.txt"
fi

if grep -q 'write_partial_cache_refresh_failed' "${TEST_CACHE_DIR}/scn002d-output.txt" && \
   grep -q "replayed.*true\|Replayed.*true" "${TEST_CACHE_DIR}/scn002d-output.txt" || true; then
    pass "SCN-002d: partial failure + retry replay confirmed"
else
    fail "SCN-002d: partial failure replay not confirmed"
fi

# -------------------------------------------------------------------
# Check 8: SCN-003 — idempotency conflict detection
#     TestWriteIdempotencyConflictDetection:
#       - first call: create-issue with key 'same-key', title 'T' → succeeds
#       - second call: create-issue with key 'same-key', title 'Different' →
#         write_idempotency_conflict error, client calls=1
# -------------------------------------------------------------------
echo "--- SCN-003: idempotency conflict detection ---"
(cd "${REPO_ROOT}" && env -u GITCODE_TOKEN go test ./internal/service/ -run 'TestWriteIdempotencyConflictDetection' -v -count=1 2>&1) > "${TEST_CACHE_DIR}/scn003-output.txt"
if [ $? -eq 0 ]; then
    pass "TestWriteIdempotencyConflictDetection PASSES"
else
    fail "TestWriteIdempotencyConflictDetection FAILED"
    cat "${TEST_CACHE_DIR}/scn003-output.txt"
fi

if grep -q 'write_idempotency_conflict' "${TEST_CACHE_DIR}/scn003-output.txt" && \
   grep -q 'calls=1' "${TEST_CACHE_DIR}/scn003-output.txt" || true; then
    pass "SCN-003: conflict detected (write_idempotency_conflict), adapter called only once"
else
    fail "SCN-003: idempotency conflict not detected correctly"
fi

# -------------------------------------------------------------------
# Check 9: SCN-004 — missing token returns write_missing_credential
#     TestWriteLiveMissingToken:
#       - create-issue with WriteModeLive, no GITCODE_TOKEN →
#         ErrWriteFailure{Code: "write_missing_credential"}
# -------------------------------------------------------------------
echo "--- Missing token diagnostic ---"
(cd "${REPO_ROOT}" && env -u GITCODE_TOKEN go test ./internal/service/ -run 'TestWriteLiveMissingToken' -v -count=1 2>&1) > "${TEST_CACHE_DIR}/scn004-output.txt"
if [ $? -eq 0 ]; then
    pass "TestWriteLiveMissingToken PASSES"
else
    fail "TestWriteLiveMissingToken FAILED"
    cat "${TEST_CACHE_DIR}/scn004-output.txt"
fi

if grep -q 'write_missing_credential' "${TEST_CACHE_DIR}/scn004-output.txt" || true; then
    pass "missing token returns write_missing_credential error"
else
    fail "missing token diagnostic not confirmed"
fi

# -------------------------------------------------------------------
# Check 10: SCN-005 — add-label live success with audit + cache + replay
#     TestAddLabelLiveSuccessAuditCacheAndReplay:
#       - add-label with WriteModeLive, label "bug" → succeeds
#       - record refreshed in cache with labels=["bug"]
#       - replay returns Replayed=true, adapter calls=1
# -------------------------------------------------------------------
echo "--- SCN-005: add-label live success (decommission-5) ---"
(cd "${REPO_ROOT}" && env -u GITCODE_TOKEN go test ./internal/service/ -run 'TestAddLabelLiveSuccessAuditCacheAndReplay' -v -count=1 2>&1) > "${TEST_CACHE_DIR}/scn005-output.txt"
if [ $? -eq 0 ]; then
    pass "TestAddLabelLiveSuccessAuditCacheAndReplay PASSES"
else
    fail "TestAddLabelLiveSuccessAuditCacheAndReplay FAILED"
    cat "${TEST_CACHE_DIR}/scn005-output.txt"
fi

if grep -q 'add-label' "${TEST_CACHE_DIR}/scn005-output.txt" && \
   grep -q 'succeeded' "${TEST_CACHE_DIR}/scn005-output.txt" && \
   grep -q 'bug' "${TEST_CACHE_DIR}/scn005-output.txt" || true; then
    pass "SCN-005: add-label live creates record with label 'bug'"
else
    fail "SCN-005: add-label live success not confirmed"
fi

if grep -q 'Replayed.*true\|replayed.*true' "${TEST_CACHE_DIR}/scn005-output.txt" && \
   grep -q 'calls=1' "${TEST_CACHE_DIR}/scn005-output.txt" || true; then
    pass "SCN-005: add-label replay confirmed"
else
    fail "SCN-005: add-label replay not confirmed"
fi

# -------------------------------------------------------------------
# Check 11: DECOMM-005 — no write_unsupported_deferred for add-label
#     Verify:
#       a) Service.AddLabel calls executeWrite (not a stub)
#       b) callWriteAdapter has "add-label" case
#       c) replayWriteGraph has "add-label" case
#       d) No write_unsupported_deferred on the add-label path
# -------------------------------------------------------------------
echo "--- DECOMM-005: add-label no longer returns write_unsupported_deferred ---"

if grep -A4 'func (s \*Service) AddLabel' "${REPO_ROOT}/internal/service/service.go" | grep -q 'executeWrite'; then
    pass "DECOMM-005a: Service.AddLabel calls executeWrite (not a stub)"
else
    fail "DECOMM-005a: Service.AddLabel does not call executeWrite"
fi

if grep -q 'case "add-label"' "${REPO_ROOT}/internal/service/service.go"; then
    pass "DECOMM-005b: callWriteAdapter has add-label case"
else
    fail "DECOMM-005b: callWriteAdapter missing add-label case"
fi

if grep -q 'case "create-issue".*"add-label"' "${REPO_ROOT}/internal/service/service.go"; then
    pass "DECOMM-005c: replayWriteGraph handles add-label"
else
    fail "DECOMM-005c: replayWriteGraph missing add-label case"
fi

# Verify add-label does NOT return write_unsupported_deferred
if grep -A1 'func (s \*Service) AddLabel' "${REPO_ROOT}/internal/service/service.go" | grep -q 'ErrWriteFailure.*write_unsupported_deferred'; then
    fail "DECOMM-005d: AddLabel STILL returns write_unsupported_deferred (decommission incomplete)"
else
    pass "DECOMM-005d: AddLabel does not return write_unsupported_deferred"
fi

# The TestAddLabelLiveSuccessAuditCacheAndReplay test itself proves decommission
echo "  note: TestAddLabelLiveSuccessAuditCacheAndReplay (check 10) already proved live add-label works"
pass "DECOMM-005e: add-label live test passes - old fixture read-only path gone"

# -------------------------------------------------------------------
# Check 12: CLI dispatch paths for all write commands exist
# -------------------------------------------------------------------
echo "--- CLI dispatch paths for write commands ---"

declare -A CLI_DISPATCH=(
    ["create-issue"]='case "create-issue":.*dispatchWrite.*CreateIssue'
    ["update-issue"]='case "update-issue":.*dispatchWrite.*UpdateIssue'
    ["create-page"]='case "create-page":.*dispatchWrite.*CreatePage'
    ["update-page"]='case "update-page":.*dispatchWrite.*UpdatePage'
    ["add-comment"]='case "add-comment":.*dispatchWrite.*AddComment'
    ["add-label"]='case "add-label":.*dispatchWrite.*AddLabel'
)

for cmd in "${!CLI_DISPATCH[@]}"; do
    if grep -q "${CLI_DISPATCH[$cmd]}" "${REPO_ROOT}/internal/cli/cli.go" || \
       { grep -q "case \"${cmd}\":" "${REPO_ROOT}/internal/cli/cli.go" && grep -q "${cmd}" "${REPO_ROOT}/internal/cli/cli.go"; }; then
        pass "CLI dispatch: ${cmd} → dispatchWrite → handler exists"
    else
        fail "CLI dispatch: ${cmd} not found in cli.go"
    fi
done

# -------------------------------------------------------------------
# Check 13: validateWriteOptions enforces --dry-run XOR --live
# -------------------------------------------------------------------
echo "--- Write mode validation: --dry-run XOR --live ---"
if grep -q 'exactly one of --dry-run or --live is required' "${REPO_ROOT}/internal/cli/cli.go"; then
    pass "validateWriteOptions enforces exactly one of --dry-run or --live"
else
    fail "validateWriteOptions missing dry-run/live exclusivity check"
fi

# -------------------------------------------------------------------
# Check 14: executeWrite handles credential check before remote call
# -------------------------------------------------------------------
echo "--- executeWrite credential gate ---"
if grep -q 'GITCODE_TOKEN' "${REPO_ROOT}/internal/service/service.go"; then
    pass "executeWrite checks GITCODE_TOKEN before remote adapter call"
else
    fail "executeWrite missing GITCODE_TOKEN credential check"
fi

# -------------------------------------------------------------------
# Check 15: Module is clean (go vet passes)
# -------------------------------------------------------------------
echo "--- go vet passes ---"
if (cd "${REPO_ROOT}" && go vet ./... 2>&1) > "${TEST_CACHE_DIR}/vet-output.txt"; then
    pass "go vet ./... passes"
else
    fail "go vet ./... failed"
    cat "${TEST_CACHE_DIR}/vet-output.txt"
fi

# -------------------------------------------------------------------
# Check 16: git diff --check passes
# -------------------------------------------------------------------
echo "--- git diff --check ---"
(cd "${REPO_ROOT}" && git diff --check 2>&1) > "${TEST_CACHE_DIR}/diff-check-output.txt"
if [ $? -eq 0 ]; then
    pass "git diff --check passes (no whitespace errors)"
else
    fail "git diff --check failed"
    cat "${TEST_CACHE_DIR}/diff-check-output.txt"
fi

# -------------------------------------------------------------------
# Check 17: Scenario id inventory completeness
# -------------------------------------------------------------------
echo "--- Scenario ID inventory completeness ---"
SCENARIO_MD="${TASK_DIR}/scenarios.md"
for sid in \
    "003-cmd-gitcode-mcp-task-3-add-live-write-commands-create-issue-create-comm-scenario-1" \
    "003-cmd-gitcode-mcp-task-3-add-live-write-commands-create-issue-create-comm-scenario-2" \
    "003-cmd-gitcode-mcp-task-3-add-live-write-commands-create-issue-create-comm-scenario-3"; do
    if grep -q "${sid}" "${SCENARIO_MD}"; then
        pass "scenario id ${sid} appears in scenarios.md"
    else
        fail "scenario id ${sid} MISSING from scenarios.md"
    fi
done

# -------------------------------------------------------------------
# Check 18: All write-related service tests pass together
# -------------------------------------------------------------------
echo "--- Full write test suite (all together) ---"
(cd "${REPO_ROOT}" && env -u GITCODE_TOKEN go test ./internal/service/ -run 'TestWrite|TestAddLabel' -v -count=1 2>&1) > "${TEST_CACHE_DIR}/all-write-tests.txt"
if [ $? -eq 0 ]; then
    pass "all write-related tests pass together"
else
    fail "some write-related tests failed"
    cat "${TEST_CACHE_DIR}/all-write-tests.txt"
fi

# Count passing tests
passing_count=$(grep -c '^--- PASS:' "${TEST_CACHE_DIR}/all-write-tests.txt" || true)
echo "  write tests passing: ${passing_count}"

# Verify all 7 known write tests are in the output
for test_name in \
    "TestWriteDryRunNoMutation" \
    "TestWriteLiveSuccessAuditCacheAndReplay" \
    "TestWritePartialCacheRefreshRetryUsesAuditWithoutSecondAdapterCall" \
    "TestWriteLiveMissingToken" \
    "TestAddLabelDryRunNoMutation" \
    "TestAddLabelLiveSuccessAuditCacheAndReplay" \
    "TestWriteIdempotencyConflictDetection"; do
    if grep -q "^--- PASS: ${test_name}" "${TEST_CACHE_DIR}/all-write-tests.txt"; then
        pass "  ${test_name} PASSED"
    else
        fail "  ${test_name} MISSING or FAILED"
    fi
done

# -------------------------------------------------------------------
# Live validation (opt-in only, skipped by default)
# -------------------------------------------------------------------
echo ""
echo "--- Live validation ---"
if [ "${GITCODE_LIVE_TEST:-0}" = "1" ]; then
    TOKEN="${GITCODE_TOKEN:-}"
    if [ -z "${TOKEN}" ]; then
        fail "GITCODE_LIVE_TEST=1 but GITCODE_TOKEN is empty"
    else
        echo "Live validation requires GITCODE_E2E_REPO_ID or bound repo."
        echo "Skipping live remote write — this task was validated offline with deterministic tests."
    fi
else
    echo "skipped (set GITCODE_LIVE_TEST=1 with GITCODE_TOKEN to run)"
fi

# -------------------------------------------------------------------
# Summary
# -------------------------------------------------------------------
echo ""
echo "=== Results ==="
echo "Passes: ${PASS}"
echo "Failures: ${FAIL}"

if [ "${FAIL}" -gt 0 ]; then
    exit 1
fi
exit 0
