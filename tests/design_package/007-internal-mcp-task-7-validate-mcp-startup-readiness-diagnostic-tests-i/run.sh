#!/usr/bin/env bash
set -euo pipefail

# Validation run script for task 007:
# Validate MCP startup/readiness diagnostic tests (i
#
# This script runs the MCP package tests to validate startup/readiness
# diagnostic behaviors. All checks are offline (no network, no external
# dependencies). Mocks are used only at the factory level (StartupDiagnosticFromError);
# the actual MCP JSON-RPC path is exercised through RPCHandler.Handle().

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
FAILURES=0

echo "=== Scenario 1: go test ./internal/mcp/... passes ==="
if go test ./internal/mcp/... 2>&1; then
  echo "PASS: go test ./internal/mcp/... exits 0"
else
  echo "FAIL: go test ./internal/mcp/... failed"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Scenario 2: Each failure scenario produces typed diagnostic in tools/list capability metadata ==="
# Run only the startup-diagnostic-specific tests and verify each passes individually
for test in \
  TestStartupDiagnosticInjection \
  TestStartupDiagnosticSchemaIncompatible \
  TestStartupDiagnosticCacheLockContention \
  TestStartupDiagnosticStartupFailure \
  TestStartupDiagnosticAllScenarios \
  TestStartupDiagnosticRemediationText; do
  echo -n "  $test ... "
  if go test ./internal/mcp/... -run "^${test}$" -count=1 2>&1; then
    echo "PASS"
  else
    echo "FAIL"
    FAILURES=$((FAILURES + 1))
  fi
done

echo ""
echo "=== Scenario 3: Doctor tool call returns structured diagnostic body with actionable remediation text ==="
# This is covered by the same tests (each verifies both tools/list and doctor)
# Run a targeted check for the table-driven test which covers all 4 classes at once
echo -n "  TestStartupDiagnosticAllScenarios (doctor validation) ... "
output=$(go test ./internal/mcp/... -run "^TestStartupDiagnosticAllScenarios$" -count=1 -v 2>&1)
exit_code=$?
if [ "$exit_code" -eq 0 ]; then
  subtest_count=$(echo "$output" | grep -c "^=== RUN   TestStartupDiagnosticAllScenarios/SCN-" || true)
  pass_count=$(echo "$output" | grep -c "^    --- PASS:" || true)
  if [ "$subtest_count" -eq 4 ] && [ "$pass_count" -eq 4 ]; then
    echo "PASS (4/4 sub-scenarios)"
  else
    echo "FAIL (subtests=$subtest_count, passes=$pass_count)"
    FAILURES=$((FAILURES + 1))
  fi
else
  echo "FAIL (exit $exit_code)"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Cross-package regression check ==="
if go test ./... 2>&1; then
  echo "PASS: go test ./... exits 0"
else
  echo "FAIL: go test ./... failed"
  FAILURES=$((FAILURES + 1))
fi

echo ""
if [ "$FAILURES" -eq 0 ]; then
  echo "All validation scenarios passed."
  exit 0
else
  echo "$FAILURES validation scenario(s) failed."
  exit 1
fi
