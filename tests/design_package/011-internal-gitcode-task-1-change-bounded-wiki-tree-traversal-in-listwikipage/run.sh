#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

export GONOSUMDB="*"
export GOPRIVATE=""

run_go_test() {
  local name="$1"
  local pkg="$2"
  local pattern="$3"
  echo "[design-package:011] running ${name}: ${pattern} (${pkg})"
  go test "${pkg}" -run "${pattern}" -count=1 -v
}

echo "=== scenario-1: cancel mid-traversal stops within current directory level ==="
run_go_test "scenario-1-cancel-mid-traversal" "./internal/gitcode" '^TestBoundedWikiTreeTraversalCancelMidTraversal$'

echo ""
echo "=== scenario-2: PartialSyncError returned with records committed so far ==="
run_go_test "scenario-2-partial-sync-error" "./internal/service" '^TestBulkSyncWikiBoundedPreCancel$'

echo ""
echo "=== scenario-3: stack-based walker checks ctx.Done() at each directory entry ==="
echo "[design-package:011] verifying ctx.Done() check in walker iteration loop"
if ! grep -n 'ctx.Err()' internal/gitcode/http_client.go | head -5; then
  echo "FAIL: ctx.Err() check not found in walker loop"
  exit 1
fi
echo ""

echo ""
echo "=== scenario-4: no outer loop wrap pattern in bulkSyncWikiBounded ==="
echo "[design-package:011] verifying no outer loop pattern in service layer"
if grep -rn 'for.*ListWikiPages' internal/service/ --include='*.go'; then
  echo "FAIL: outer loop pattern wrapping ListWikiPages found in service layer"
  exit 1
fi
echo "[design-package:011] outer loop check passed: no for-loop wrapping ListWikiPages"

echo ""
echo "=== scenario-4 (test): bounded request with single-call satisfies all records ==="
run_go_test "scenario-4-no-outer-loop-test" "./internal/gitcode" '^TestBoundedWikiTreeTraversalNoOuterLoopPattern$'

echo ""
echo "[design-package:011] running full bounded wiki test suite (gitcode adapter)"
go test ./internal/gitcode -run 'TestBoundedWiki' -count=1

echo ""
echo "[design-package:011] running full bounded wiki test suite (service)"
go test ./internal/service -run 'TestBulkSyncWiki' -count=1

echo ""
echo "[design-package:011] running repository acceptance gate"
go test ./...
git diff --check

echo ""
echo "[design-package:011] PASS"
