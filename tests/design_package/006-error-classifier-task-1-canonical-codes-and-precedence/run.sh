#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
FAILURES=0

echo "=== Validating 006-error-classifier-task-1-canonical-codes-and-precedence ==="
cd "$ROOT_DIR"

run_check() {
    local label="$1"
    shift
    echo "[$label] $*"
    if "$@"; then
        echo "  PASS: $label"
    else
        echo "  FAIL: $label"
        FAILURES=$((FAILURES + 1))
    fi
}

run_go_test_must_run() {
    local label="$1"
    shift
    local output
    echo "[$label] go test $*"
    set +e
    output="$(go test "$@" -v 2>&1)"
    local status=$?
    set -e
    printf '%s\n' "$output"
    if [ "$status" -eq 0 ] && ! grep -q 'testing: warning: no tests to run' <<<"$output" && grep -q -- '--- PASS:' <<<"$output"; then
        echo "  PASS: $label"
    else
        echo "  FAIL: $label"
        FAILURES=$((FAILURES + 1))
    fi
}

# Scenario 1: diagnostics runtime tests for canonical live-http precedence.
run_go_test_must_run "scenario-1 diagnostics precedence" ./internal/diagnostics/... -run 'TestClassifierLivePrecedenceAndHTTPInvariants|TestClassifierLiveDecommissionInvariant|TestClassifierLegacyCodeNormalization|TestClassifierFailureSourceMapping' -count=1

# Ensure the named acceptance cases are actually present in runtime tests, not only documented.
required_diagnostics=(
    'SCN-DIAG-PRECEDENCE-01'
    'SCN-DIAG-PRECEDENCE-02'
    'SCN-DIAG-PRECEDENCE-03'
    'SCN-DIAG-PRECEDENCE-04'
    'SCN-DIAG-PRECEDENCE-05'
    'SCN-DIAG-PRECEDENCE-06'
    'SCN-DIAG-PRECEDENCE-07'
    'SCN-DIAG-PRECEDENCE-08'
    'SCN-DIAG-PRECEDENCE-09'
    'SCN-DIAG-PRECEDENCE-10'
    'SCN-DIAG-PRECEDENCE-11'
    'SCN-DIAG-PRECEDENCE-12'
    'SCN-DIAG-DECOM-01'
    'SCN-DIAG-LEGACY-NORMALIZATION-01'
    'SCN-DIAG-FAILURE-SOURCE-01'
)
for id in "${required_diagnostics[@]}"; do
    if grep -R -- "$id" internal/diagnostics/*_test.go >/dev/null 2>&1; then
        echo "  PASS: diagnostics runtime case present: $id"
    else
        echo "  FAIL: diagnostics runtime case missing: $id"
        FAILURES=$((FAILURES + 1))
    fi
done

# Scenario 2: product-path tests must exist and execute through CLI and MCP packages.
# These concrete subtests fail validation if implementation only added unit-level classifier tests.
if grep -R -- 'SCN-CLI-ERROR-OUTPUT-01' cmd/gitcode-mcp/*_test.go >/dev/null 2>&1 && grep -R -- 'failure_class' cmd/gitcode-mcp/*_test.go >/dev/null 2>&1; then
    run_go_test_must_run "scenario-2 CLI product-path failure_class" ./cmd/gitcode-mcp/... -run 'TestCLIStartupPlanSelectsLiveProvider/.*SCN-CLI-ERROR-OUTPUT-01' -count=1
else
    echo "  FAIL: scenario-2 CLI product-path test SCN-CLI-ERROR-OUTPUT-01 asserting failure_class is absent"
    FAILURES=$((FAILURES + 1))
fi

if grep -R -- 'SCN-MCP-ERROR-OUTPUT-01' internal/mcp/*_test.go >/dev/null 2>&1 && grep -R -- 'failure_class' internal/mcp/*_test.go >/dev/null 2>&1; then
    run_go_test_must_run "scenario-2 MCP product-path failure_class" ./internal/mcp/... -run 'TestMCPErrorOutputCanonicalFailureClass' -count=1
else
    echo "  FAIL: scenario-2 MCP product-path test SCN-MCP-ERROR-OUTPUT-01 asserting failure_class is absent"
    FAILURES=$((FAILURES + 1))
fi

# Offline package sanity for the explicitly accepted diagnostics package.
run_check "diagnostics package full offline test" go test ./internal/diagnostics/... -count=1

# Whitespace validation for the materialized validation files and current implementation diff.
run_check "git diff whitespace check" git diff --check

if [ "$FAILURES" -ne 0 ]; then
    echo "Validation failed with $FAILURES failure(s)."
    exit 1
fi

echo "Validation passed."
