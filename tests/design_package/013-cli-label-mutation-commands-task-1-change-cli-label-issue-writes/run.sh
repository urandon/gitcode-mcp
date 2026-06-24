#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$REPO_ROOT"

echo "=== Validation: 013-cli-label-mutation-commands-task-1-change-cli-label-issue-writes ==="
echo ""

failures=0

run_test() {
  local pkg="$1"
  local pattern="$2"
  echo "--- $pkg :: $pattern ---"
  if go test "$pkg" -count=1 -run "$pattern" >/dev/null 2>&1; then
    echo "PASS: $pkg :: $pattern"
  else
    echo "FAIL: $pkg :: $pattern"
    go test "$pkg" -count=1 -v -run "$pattern" 2>&1 | tail -40
    failures=$((failures + 1))
  fi
}

# ==============================================================================
# Scenario 1: create-issue labels as native JSON array
# ==============================================================================
echo "== Scenario 1: Create-issue labels sent as native JSON array in request body =="
echo ""

run_test "./internal/gitcode/..." "TestScenario013001CreateIssueLabelsAsNativeJSONArray"
run_test "./internal/gitcode/..." "TestLabel011CreateRequestLabelString"
run_test "./internal/gitcode/..." "TestLabel013ArrayLabelsAccepted"

# Verify EncodeIssueLabels produces valid JSON array
echo ""
echo "--- EncodeIssueLabels unit test ---"
run_test "./internal/gitcode/..." "TestLabel001EncodeJSONString"
run_test "./internal/gitcode/..." "TestLabel002EncodeEmpty"
run_test "./internal/gitcode/..." "TestLabel003EncodeTrimDrops"
run_test "./internal/gitcode/..." "TestScenario013008EncodeIssueLabelsOutputIsJSONArray"

# ==============================================================================
# Scenario 2: update-issue labels as native JSON array
# ==============================================================================
echo ""
echo "== Scenario 2: Update-issue labels sent as native JSON array in request body =="
echo ""

run_test "./internal/gitcode/..." "TestScenario013002UpdateIssueLabelsAsNativeJSONArray"

# ==============================================================================
# Scenario 3: add-label returns unsupported diagnostic
# ==============================================================================
echo ""
echo "== Scenario 3: Add-label returns unsupported diagnostic, old route unreachable =="
echo ""

run_test "./internal/service/..." "TestAddLabelDryRunNoMutation"
run_test "./internal/service/..." "TestAddLabelLiveUnsupportedCapability"
run_test "./internal/gitcode/..." "TestScenario013004AddLabelReturnsUnsupportedCapability"
run_test "./internal/gitcode/..." "TestScenario013006AddLabelEndpointAbsentFromProvider"

# ==============================================================================
# Scenario 4: Full offline test suite + git diff --check
# ==============================================================================
echo ""
echo "== Scenario 4: go test ./... and git diff --check pass offline =="
echo ""

echo "--- git diff --check ---"
if git diff --check; then
  echo "PASS: git diff --check"
else
  echo "FAIL: git diff --check"
  failures=$((failures + 1))
fi

echo ""
echo "--- go test ./... (full offline suite) ---"
if go test ./... -count=1 >/dev/null 2>&1; then
  echo "PASS: go test ./..."
else
  echo "FAIL: go test ./..."
  go test ./... -count=1 2>&1 | tail -40
  failures=$((failures + 1))
fi

# ==============================================================================
# Additional regression and decommission checks
# ==============================================================================
echo ""
echo "== Decommission regression checks =="
echo ""

echo "--- Label normalization tests ---"
run_test "./internal/gitcode/..." "TestLabel004NormalizeValid"
run_test "./internal/gitcode/..." "TestLabel005NormalizeEmptyInput"
run_test "./internal/gitcode/..." "TestLabel006NormalizeMissingID"
run_test "./internal/gitcode/..." "TestLabel007NormalizeMissingName"
run_test "./internal/gitcode/..." "TestLabel008NormalizeSingle"
run_test "./internal/gitcode/..." "TestLabel009NormalizeSingleInvalid"
run_test "./internal/gitcode/..." "TestLabel014SchemaDecodeDistinctFromTransport"
run_test "./internal/gitcode/..." "TestLabel015ObjectLabelWithMissingIDReturnsSchemaDecode"
run_test "./internal/gitcode/..." "TestLabel010IssueResponseNormalized"
run_test "./internal/gitcode/..." "TestLabel010FixtureStringsStillWork"

echo ""
echo "--- No-labels field omission ---"
run_test "./internal/gitcode/..." "TestScenario013003CreateIssueNoLabelsOmitsField"
run_test "./internal/gitcode/..." "TestLabel012CreateRequestEmptyLabels"

echo ""
echo "--- Create issue with labels normalized in response ---"
run_test "./internal/gitcode/..." "TestScenario013007CreateIssueWithLabelsNormalizedInResponse"

echo ""
echo "--- CLI write commands dispatch to service ---"
run_test "./internal/cli/..." "TestQueryCommandsUseServiceOnly"

# ==============================================================================
# Final verdict
# ==============================================================================
echo ""
if [ "$failures" -eq 0 ]; then
  echo "=== ALL VALIDATION CHECKS PASSED ==="
  exit 0
else
  echo "=== VALIDATION FAILED: $failures check(s) failed ==="
  exit 1
fi
