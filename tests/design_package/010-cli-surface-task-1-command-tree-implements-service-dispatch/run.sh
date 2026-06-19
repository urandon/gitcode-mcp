#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
FAILURES=0

echo "=== Scenario 1: TestSearchJSON ==="
if go test ./internal/cli/... -run TestSearchJSON -count=1; then
  echo "PASS: TestSearchJSON"
else
  echo "FAIL: TestSearchJSON"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Scenario 2: TestGetSource ==="
if go test ./internal/cli/... -run TestGetSource -count=1; then
  echo "PASS: TestGetSource"
else
  echo "FAIL: TestGetSource"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Scenario 3: TestAllCommandsRegistered ==="
if go test ./internal/cli/... -run TestAllCommandsRegistered -count=1; then
  echo "PASS: TestAllCommandsRegistered"
else
  echo "FAIL: TestAllCommandsRegistered"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Scenario 4: TestMinimumReplacementBar ==="
if go test ./internal/cli/... -run TestMinimumReplacementBar -count=1; then
  echo "PASS: TestMinimumReplacementBar"
else
  echo "FAIL: TestMinimumReplacementBar"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Decommission Invariants ==="

echo ""
echo "=== Decommission-1: verify no shell subprocess spawning ==="
if rg -l 'os\.Exec|exec\.|syscall\.' internal/cli/ -- '*.go' 2>/dev/null; then
  echo "FAIL: CLI contains shell subprocess calls"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: no shell subprocess calls in CLI"
fi

echo ""
echo "=== Decommission-3: verify minimum replacement bar offline ==="
# TestQueryCommandsUseServiceOnly exercises all 20 commands via spy service
if go test ./internal/cli/... -run TestQueryCommandsUseServiceOnly -count=1; then
  echo "PASS: TestQueryCommandsUseServiceOnly"
else
  echo "FAIL: TestQueryCommandsUseServiceOnly"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Decommission-4: verify get_source includes remote_alias and link-check reports broken links ==="
# Check that renderGetText includes remote_alias field
if grep -q 'remote_alias' internal/cli/cli.go; then
  echo "PASS: get_source render includes remote_alias"
else
  echo "FAIL: get_source render missing remote_alias"
  FAILURES=$((FAILURES + 1))
fi

# Check that link-check includes SuggestedAliases
if go test ./internal/cli/... -run TestLinkCheckJSON -count=1; then
  echo "PASS: TestLinkCheckJSON (includes broken links + suggested aliases)"
else
  echo "FAIL: TestLinkCheckJSON"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Full suite ==="
if go test ./... -count=1; then
  echo "PASS: full test suite"
else
  echo "FAIL: full test suite"
  FAILURES=$((FAILURES + 1))
fi

echo ""
if [ "$FAILURES" -eq 0 ]; then
  echo "ALL VALIDATIONS PASSED"
else
  echo "$FAILURES VALIDATION(S) FAILED"
  exit 1
fi
