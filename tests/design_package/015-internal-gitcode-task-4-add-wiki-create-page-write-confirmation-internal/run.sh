#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

run_go_test() {
  local name="$1"
  local pkg="$2"
  local pattern="$3"
  echo "[design-package:015] running ${name}: ${pattern} (${pkg})"
  go test "${pkg}" -run "${pattern}" -count=1 -v
}

echo "=== scenario-1: POST 201 missing path/sha, follow-up GET confirms ==="
run_go_test "scenario-1-adapter-followup-confirmation" "./internal/gitcode" '^TestScenario015WikiCreatePageFollowupConfirmation$'

echo ""
echo "--- scenario-1 service cache/http_attempted evidence ---"
run_go_test "scenario-1-service-live-write-cache" "./internal/service" '^TestS018LiveWriteConfirmedRefreshesCommentAndWiki$'

echo ""
echo "=== scenario-2: follow-up GET failure/mismatch returns write_confirmation_incomplete ==="
run_go_test "scenario-2-adapter-confirmation-failures" "./internal/gitcode" '^TestScenario015WikiCreatePageFollowupConfirmationFailure$'

echo ""
echo "=== required package gates ==="

echo "--- internal/gitcode/... ---"
go test ./internal/gitcode/... -count=1 -v 2>&1 | grep -E "PASS|FAIL|ok"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: go test ./internal/gitcode/... returned non-zero exit code"
  exit 1
fi

echo ""
echo "--- internal/service/... ---"
go test ./internal/service/... -count=1 -v 2>&1 | grep -E "PASS|FAIL|ok"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: go test ./internal/service/... returned non-zero exit code"
  exit 1
fi

echo ""
echo "--- full repository go test ./... ---"
go test ./... 2>&1 | grep -E "^ok|^FAIL"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: go test ./... returned non-zero exit code"
  exit 1
fi

echo ""
echo "--- git diff --check ---"
git diff --check
echo "[design-package:015] git diff --check passed (no whitespace violations)"

echo ""
echo "[design-package:015] PASS"
exit 0
