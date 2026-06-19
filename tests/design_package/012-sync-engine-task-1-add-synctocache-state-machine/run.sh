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

run_check "Scenario 1-2: stale status, SyncToCache update, fresh status, sync event" \
  "go test ./internal/service/... -run '^TestSyncStateMachine$' -count=1"

run_check "Scenario 3: lock contention returns typed ErrLockContention without remote fetch or mutation" \
  "go test ./internal/service/... -run '^TestSyncLockContention$' -count=1"

run_check "Scenario 4: retryable rate limit retries sync call and commits once" \
  "go test ./internal/service/... -run '^TestSyncRetry$' -count=1"

run_check "Scenario 5: idempotency replay bypasses lock, remote fetch, and duplicate mutations" \
  "go test ./internal/service/... -run '^TestSyncIdempotencyReplay$' -count=1"

run_check "Scenario 6: bounded staging failure leaves cache records unchanged" \
  "go test ./internal/service/... -run '^TestSyncBoundedStaging$' -count=1"

run_check "Acceptance command: all TestSync service scenarios" \
  "go test ./internal/service/... -run TestSync -count=1"

run_check "Offline service package regression" \
  "go test ./internal/service/... -short -count=1"

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
