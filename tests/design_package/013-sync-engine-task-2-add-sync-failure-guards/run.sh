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

run_check "Scenario 1: complete failure-mode suite through SyncToCache" \
  "go test ./internal/service/... -run '^TestFailureModes$' -count=1"

run_check "Scenario 2: timeout guard preserves cache and records failed event" \
  "go test ./internal/service/... -run '^TestFailureModes$/^failure-timeout-network-unavailable$' -count=1"

run_check "Scenario 3: rate-limit guard preserves RetryAfter and cache state" \
  "go test ./internal/service/... -run '^TestFailureModes$/^failure-rate-limited-retry-after$' -count=1"

run_check "Scenario 3 support: retry-after controls retry before successful commit" \
  "go test ./internal/service/... -run '^TestSyncRetry$' -count=1"

run_check "Scenario 4: partial JSON guard" \
  "go test ./internal/service/... -run '^TestFailureModes$/^failure-partial-response$' -count=1"

run_check "Scenario 4: auth expiry guard" \
  "go test ./internal/service/... -run '^TestFailureModes$/^failure-auth-expired$' -count=1"

run_check "Scenario 4: remote id collision guard" \
  "go test ./internal/service/... -run '^TestFailureModes$/^failure-remote-id-collision$' -count=1"

run_check "Scenario 4: cache corruption guard" \
  "go test ./internal/service/... -run '^TestFailureModes$/^failure-cache-corruption$' -count=1"

run_check "Scenario 4: lock contention guard" \
  "go test ./internal/service/... -run '^TestFailureModes$/^failure-lock-contention$' -count=1"

run_check "Scenario 4: missing remote record guard" \
  "go test ./internal/service/... -run '^TestFailureModes$/^failure-missing-remote-record$' -count=1"

run_check "Scenario 4: oversized payload guard" \
  "go test ./internal/service/... -run '^TestFailureModes$/^failure-oversized-payload$' -count=1"

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
