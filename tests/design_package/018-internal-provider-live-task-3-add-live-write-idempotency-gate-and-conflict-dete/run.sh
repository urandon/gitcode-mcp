#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

export GITCODE_LIVE_TEST=""
export GITCODE_TOKEN=""

run_go_test() {
  local package="$1"
  shift
  go test "$package" "$@"
}

run_go_test ./internal/service \
  -run '^(TestS018LiveWriteUsesHTTPProviderAndRefreshesCache|TestS018LiveWriteConflictMaps409|TestWriteDryRunNoMutation|TestWriteLiveSuccessAuditCacheAndReplay|TestWritePartialCacheRefreshRetryUsesAuditWithoutSecondAdapterCall|TestWriteIdempotencyConflictDetection)$' \
  -count=1

run_go_test ./internal/gitcode \
  -run '^(TestFailureModes/conflict|TestWriteIdempotency)$' \
  -count=1

go test ./internal/provider/live -count=1
