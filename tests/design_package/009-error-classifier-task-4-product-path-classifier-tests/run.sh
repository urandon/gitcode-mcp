#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$REPO_ROOT"

echo "=== Validation: 009-error-classifier-task-4-product-path-classifier-tests ==="
echo ""

failures=0

run_test() {
  local pkg="$1"
  echo "--- $pkg ---"
  if go test "$pkg" -count=1 >/dev/null 2>&1; then
    echo "PASS: $pkg"
  else
    echo "FAIL: $pkg"
    go test "$pkg" -count=1 2>&1 | tail -20
    failures=$((failures + 1))
  fi
}

# Scenario 1: Full offline test suite
echo "== Scenario 1: go test ./... without credentials, network, SSH agent, or Keychain =="
run_test "./internal/diagnostics/..."
run_test "./internal/service/..."
run_test "./cmd/gitcode-mcp/..."
run_test "./..."

echo ""
echo "--- git diff --check ---"
if git diff --check; then
  echo "PASS: git diff --check"
else
  echo "FAIL: git diff --check"
  failures=$((failures + 1))
fi

# Scenario 2: Verify canonical failure_class in CLI and MCP product paths
echo ""
echo "== Scenario 2: CLI and MCP canonical failure_class verification =="

echo "--- CLI error output scenarios ---"
cli_failure_modes=(
  "SCN-CLI-ERROR-OUTPUT-400"
  "SCN-CLI-ERROR-OUTPUT-401"
  "SCN-CLI-ERROR-OUTPUT-404"
  "SCN-CLI-ERROR-OUTPUT-409"
  "SCN-CLI-ERROR-OUTPUT-413"
  "SCN-CLI-ERROR-OUTPUT-429"
  "SCN-CLI-ERROR-OUTPUT-MALFORMED-JSON"
  "SCN-CLI-ERROR-OUTPUT-SCHEMA-MISMATCH"
  "SCN-CLI-ERROR-OUTPUT-PARTIAL-RESPONSE"
  "SCN-CLI-ERROR-OUTPUT-TIMEOUT"
  "SCN-CLI-ERROR-OUTPUT-500"
)

for mode in "${cli_failure_modes[@]}"; do
  if go test ./cmd/gitcode-mcp/... -count=1 -run "TestCLIStartupPlanSelectsLiveProvider/$mode" >/dev/null 2>&1; then
    echo "PASS: $mode"
  else
    echo "FAIL: $mode"
    go test ./cmd/gitcode-mcp/... -count=1 -v -run "TestCLIStartupPlanSelectsLiveProvider/$mode" 2>&1 | tail -20
    failures=$((failures + 1))
  fi
done

echo ""
echo "--- MCP error output scenarios ---"
mcp_failure_modes=(
  "SCN-MCP-ERROR-OUTPUT-401"
  "SCN-MCP-ERROR-OUTPUT-400"
  "SCN-MCP-ERROR-OUTPUT-404"
  "SCN-MCP-ERROR-OUTPUT-409"
  "SCN-MCP-ERROR-OUTPUT-413"
  "SCN-MCP-ERROR-OUTPUT-429"
  "SCN-MCP-ERROR-OUTPUT-MALFORMED-JSON"
  "SCN-MCP-ERROR-OUTPUT-SCHEMA-MISMATCH"
  "SCN-MCP-ERROR-OUTPUT-PARTIAL-RESPONSE"
  "SCN-MCP-ERROR-OUTPUT-LOCAL-BODY-LIMIT"
  "SCN-MCP-ERROR-OUTPUT-TIMEOUT"
  "SCN-MCP-ERROR-OUTPUT-500"
)

for mode in "${mcp_failure_modes[@]}"; do
  if go test ./internal/mcp/... -count=1 -run "TestMCPErrorOutputCanonicalFailureClass/$mode" >/dev/null 2>&1; then
    echo "PASS: $mode"
  else
    echo "FAIL: $mode"
    go test ./internal/mcp/... -count=1 -v -run "TestMCPErrorOutputCanonicalFailureClass/$mode" 2>&1 | tail -20
    failures=$((failures + 1))
  fi
done

echo ""
echo "--- Service wrapper classification scenarios ---"
wrap_modes=(
  "SCN-DIAG-PRODUCT-WRAP-01"
  "SCN-DIAG-PRODUCT-WRAP-02"
  "SCN-DIAG-PRODUCT-WRAP-03"
  "SCN-DIAG-PRODUCT-WRAP-04"
  "SCN-DIAG-PRODUCT-WRAP-05"
  "SCN-DIAG-PRODUCT-WRAP-06"
  "SCN-DIAG-PRODUCT-WRAP-07"
  "SCN-DIAG-PRODUCT-WRAP-08"
  "SCN-DIAG-PRODUCT-WRAP-09"
)

for mode in "${wrap_modes[@]}"; do
  if go test ./internal/service/... -count=1 -run "TestProductPathWrappedGitCodeErrorsClassify/$mode" >/dev/null 2>&1; then
    echo "PASS: $mode"
  else
    echo "FAIL: $mode"
    go test ./internal/service/... -count=1 -v -run "TestProductPathWrappedGitCodeErrorsClassify/$mode" 2>&1 | tail -20
    failures=$((failures + 1))
  fi
done

echo ""
echo "--- Decommission invariant scenarios ---"
decom_modes=(
  "SCN-DIAG-DECOM-01"
  "SCN-DIAG-DECOM-02"
  "SCN-DIAG-DECOM-03"
  "SCN-DIAG-DECOM-04"
  "SCN-DIAG-DECOM-05"
)

for mode in "${decom_modes[@]}"; do
  if go test ./internal/diagnostics/... -count=1 -run "TestClassifierLiveDecommissionInvariant/$mode" >/dev/null 2>&1; then
    echo "PASS: $mode"
  else
    echo "FAIL: $mode"
    go test ./internal/diagnostics/... -count=1 -v -run "TestClassifierLiveDecommissionInvariant/$mode" 2>&1 | tail -20
    failures=$((failures + 1))
  fi
done

echo ""
if [ "$failures" -eq 0 ]; then
  echo "=== ALL VALIDATION CHECKS PASSED ==="
  exit 0
else
  echo "=== VALIDATION FAILED: $failures check(s) failed ==="
  exit 1
fi
