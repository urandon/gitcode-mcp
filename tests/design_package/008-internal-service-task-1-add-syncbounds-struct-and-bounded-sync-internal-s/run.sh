#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

export GONOSUMDB="*"
export GOPRIVATE=""

run_go_test() {
  local name="$1"
  local pattern="$2"
  echo "[design-package:008] running ${name}: ${pattern}"
  go test ./internal/service -run "${pattern}" -count=1
}

run_go_test "scenario-1-cancel-before-page-4" '^TestBulkSyncIssuesBoundedCancelMidway(_CancelAfterPage3Progress)?$'
run_go_test "scenario-2-timeout-diagnostic" '^TestBulkSyncIssuesBoundedTimeout$'
run_go_test "scenario-3-progress-events" '^TestBulkSyncIssuesBoundedProgressEvents$'
run_go_test "decommission-3-wiki-bounded-cancel" '^TestBulkSyncWikiBoundedCancelMidPage$'

echo "[design-package:008] running focused bounded-sync regression set"
go test ./internal/service -run 'Bounded|SyncBounds|Progress|Partial' -count=1

echo "[design-package:008] running repository acceptance gate"
go test ./...
git diff --check
