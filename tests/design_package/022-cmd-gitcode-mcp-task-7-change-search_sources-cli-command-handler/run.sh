#!/usr/bin/env bash
set -euo pipefail

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

run_cmd() {
    local _ec=0
    out=$("${BINARY}" "$@" 2>"${TEST_CACHE_DIR}/stderr.$$") || _ec=$?
    ec=${_ec}
    err=$(cat "${TEST_CACHE_DIR}/stderr.$$")
}

echo "=== Validation: Change search_sources CLI command handler ==="
echo "Task directory: ${TASK_DIR}"
echo ""

# -------------------------------------------------------------------
# Check 0: Validation scope — no root binary created
# -------------------------------------------------------------------
echo "--- Validation scope check ---"
if [ -f "${REPO_ROOT}/gitcode-mcp" ]; then
    echo "  note: pre-existing root gitcode-mcp exists (not created by this validation)"
fi
pass "validation writes only to task directory: ${TASK_DIR}"

# -------------------------------------------------------------------
# Check 1: Binary builds correctly in task directory
# -------------------------------------------------------------------
echo "--- Binary build ---"
rm -f "${BINARY}"
if (cd "${REPO_ROOT}" && go build -o "${BINARY}" ./cmd/gitcode-mcp/); then
    pass "binary builds into task directory"
else
    fail "binary build failed"
    echo "EXIT: $FAIL failures, $PASS passes"
    exit 1
fi

# -------------------------------------------------------------------
# Check 2: go test ./... passes offline
# -------------------------------------------------------------------
echo "--- go test ./... passes offline ---"
_test_ec=0
(cd "${REPO_ROOT}" && go test ./... 2>&1) > "${TEST_CACHE_DIR}/test-output.txt" || _test_ec=$?
if [ "${_test_ec}" -eq 0 ]; then
    pass "go test ./... passes offline (all packages ok)"
else
    fail "go test ./... failed offline"
    cat "${TEST_CACHE_DIR}/test-output.txt"
fi

# -------------------------------------------------------------------
# Check 3: SCN — search_sources matching and non-matching CLI path
# -------------------------------------------------------------------
echo "--- Existing Go test: search_sources CLI dispatch ---"
(cd "${REPO_ROOT}" && go test ./internal/cli/ -run 'TestSearchSourcesCommandJSON|TestSearchSourcesCommandEmptyJSON' -count=1 -v 2>&1) > "${TEST_CACHE_DIR}/cli-search-sources-tests.txt"
if [ $? -eq 0 ]; then
    pass "search_sources CLI tests pass: matching query returns non-empty JSON results; non-matching query exits 0 with empty results"
else
    fail "search_sources CLI tests failed"
    cat "${TEST_CACHE_DIR}/cli-search-sources-tests.txt"
fi

# Verify the expected tests actually ran.
if grep -q 'RUN   TestSearchSourcesCommandJSON' "${TEST_CACHE_DIR}/cli-search-sources-tests.txt" && grep -q 'RUN   TestSearchSourcesCommandEmptyJSON' "${TEST_CACHE_DIR}/cli-search-sources-tests.txt"; then
    pass "both required search_sources CLI scenario tests executed"
else
    fail "required search_sources CLI tests did not execute"
    cat "${TEST_CACHE_DIR}/cli-search-sources-tests.txt"
fi

# -------------------------------------------------------------------
# Check 4: Alias/service dispatch guard
# -------------------------------------------------------------------
echo "--- Existing Go test: search_sources routes to service SearchSources ---"
(cd "${REPO_ROOT}" && go test ./internal/cli/ -run 'TestQueryCommandsUseServiceOnly|TestSearchJSON|TestMinimumReplacementBar' -count=1 -v 2>&1) > "${TEST_CACHE_DIR}/cli-routing-tests.txt"
if [ $? -eq 0 ]; then
    pass "CLI routing tests pass: search_sources is a service-backed command alias and existing search behavior is preserved"
else
    fail "CLI routing tests failed"
    cat "${TEST_CACHE_DIR}/cli-routing-tests.txt"
fi

# -------------------------------------------------------------------
# Check 5: Service/cache path still satisfies outcome and decommission
# -------------------------------------------------------------------
echo "--- Existing Go test: service/cache SearchSources behavior ---"
_svc_ec=0
(cd "${REPO_ROOT}" && go test ./internal/service/ -run 'TestSearchSources|TestQueryEdgeCases|TestQueryMethodsDoNotUseLiveNetwork' -count=1 -v 2>&1) > "${TEST_CACHE_DIR}/svc-tests.txt" || _svc_ec=$?
_cache_ec=0
(cd "${REPO_ROOT}" && go test ./internal/cache/ -run 'SearchSources|TestMinimumReplacementCacheState|TestSearchFallbackParity' -count=1 -v 2>&1) > "${TEST_CACHE_DIR}/cache-tests.txt" || _cache_ec=$?
if [ "${_svc_ec}" -eq 0 ] && [ "${_cache_ec}" -eq 0 ]; then
    pass "service/cache SearchSources tests pass: matching query non-empty, no-match empty/no error, no live network"
else
    fail "service/cache SearchSources tests failed"
    cat "${TEST_CACHE_DIR}/svc-tests.txt"
    cat "${TEST_CACHE_DIR}/cache-tests.txt"
fi

# -------------------------------------------------------------------
# Check 6: Standalone binary command recognition/help path
# -------------------------------------------------------------------
echo "--- Standalone binary: search_sources --help ---"
run_cmd search_sources --help
if [ "${ec}" -eq 0 ] && echo "${out}" | grep -qi "usage" && echo "${out}" | grep -q "search_sources"; then
    pass "search_sources --help exit 0 with Usage text"
else
    fail "search_sources --help exit=${ec} out=${out} err=${err}"
fi

run_cmd search_sources -h
if [ "${ec}" -eq 0 ] && [ -n "${out}" ]; then
    pass "search_sources -h exit 0 with non-empty output"
else
    fail "search_sources -h exit=${ec} out=${out} err=${err}"
fi

# Unknown-command regression guard: search_sources must not be unknown.
run_cmd search_sources --help
if echo "${err}${out}" | grep -qi "unknown command"; then
    fail "search_sources is still treated as an unknown command"
else
    pass "search_sources is recognized by standalone binary command parser"
fi

# -------------------------------------------------------------------
# Check 7: Scenario ID inventory completeness
# -------------------------------------------------------------------
echo "--- Scenario ID inventory completeness ---"
SCENARIO_MD="${TASK_DIR}/scenarios.md"
SID="022-cmd-gitcode-mcp-task-7-change-search_sources-cli-command-handler-scenario-1"
if grep -q "${SID}" "${SCENARIO_MD}"; then
    pass "scenario id ${SID} appears in scenarios.md"
else
    fail "scenario id ${SID} MISSING from scenarios.md"
fi

# -------------------------------------------------------------------
# Check 8: Production code inspection — search_sources dispatch
# -------------------------------------------------------------------
echo "--- Production code: search_sources dispatch verification ---"
CLI_GO="${REPO_ROOT}/internal/cli/cli.go"

if grep -q '"search_sources"' "${CLI_GO}"; then
    pass "search_sources registered in commands slice"
else
    fail "search_sources not found in commands slice"
fi

if grep -q 'case "search", "search_sources"' "${CLI_GO}" || grep -q 'case.*search.*search_sources' "${CLI_GO}"; then
    pass "search_sources handled in dispatch switch"
else
    fail "search_sources not found in dispatch switch"
fi

if grep -q 'SearchSources(ctx' "${CLI_GO}"; then
    pass "SearchSources called in dispatch path"
else
    fail "SearchSources call not found in dispatch"
fi

# -------------------------------------------------------------------
# Check 9: Decommission — no cache_empty on no-match CLI test output
# -------------------------------------------------------------------
echo "--- Decommission check: no cache_empty for no-match search_sources ---"
if grep -qi "cache_empty" "${TEST_CACHE_DIR}/cli-search-sources-tests.txt" "${TEST_CACHE_DIR}/svc-tests.txt" "${TEST_CACHE_DIR}/cache-tests.txt"; then
    fail "cache_empty appeared in search_sources validation output"
else
    pass "decommission-7 verified: no cache_empty on populated-cache no-match search_sources path"
fi

# -------------------------------------------------------------------
# Check 10: git diff --check
# -------------------------------------------------------------------
echo "--- git diff --check ---"
(cd "${REPO_ROOT}" && git diff --check 2>&1) > "${TEST_CACHE_DIR}/diff-check.txt"
if [ $? -eq 0 ]; then
    pass "git diff --check clean (no whitespace errors)"
else
    fail "git diff --check found whitespace issues"
    cat "${TEST_CACHE_DIR}/diff-check.txt"
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
