#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$REPO_ROOT"

FAILURES=0

run_check() {
  local name="$1"
  local cmd="$2"
  echo ""
  echo "=== $name ==="
  if bash -c "$cmd"; then
    echo "PASS: $name"
  else
    echo "FAIL: $name"
    FAILURES=$((FAILURES + 1))
  fi
}

run_check "Scenario 1 and 2: focused minimum replacement cache state" \
  "go test ./internal/cache/... -run '^TestMinimumReplacementCacheState$' -count=1"

run_check "Scenario 2: cache graph composition without markdown indexes" \
  "go test ./internal/cache/... -run 'TestBacklinks|TestIdentityResolution|TestMinimumReplacementCacheState' -count=1"

run_check "Scenario 3: short suite includes cache scenario" \
  "go test ./... -short -count=1"

run_check "Diff whitespace check" \
  "git diff --check"

echo ""
if [ "$FAILURES" -eq 0 ]; then
  echo "ALL VALIDATIONS PASSED"
else
  echo "$FAILURES VALIDATION(S) FAILED"
  exit 1
fi
