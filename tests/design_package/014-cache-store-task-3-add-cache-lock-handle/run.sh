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

run_check "Scenario 1 and 2: required cache lock acceptance tests" \
  "go test ./internal/cache/... -run 'TestLockContention|TestLockContentionBlocksSimulatedSync' -count=1"

run_check "Scenario 1: typed contention, path, release, and reacquire" \
  "go test ./internal/cache/... -run '^TestLockContention$' -count=1"

run_check "Scenario 2: lock contention blocks simulated sync mutation" \
  "go test ./internal/cache/... -run '^TestLockContentionBlocksSimulatedSync$' -count=1"

run_check "Scenario 3: offline cache package regression" \
  "go test ./internal/cache/... -short -count=1"

run_check "Repository compile check" \
  "go build ./..."

run_check "Diff whitespace check" \
  "git diff --check"

echo ""
if [ "$FAILURES" -eq 0 ]; then
  echo "ALL VALIDATIONS PASSED"
else
  echo "$FAILURES VALIDATION(S) FAILED"
  exit 1
fi
