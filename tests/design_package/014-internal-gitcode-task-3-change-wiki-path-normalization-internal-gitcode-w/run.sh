#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

run_go_test() {
  local name="$1"
  local pkg="$2"
  local pattern="$3"
  echo "[design-package:014] running ${name}: ${pattern} (${pkg})"
  go test "${pkg}" -run "${pattern}" -count=1 -v
}

echo "=== scenario-1: Remote Home.md synced, cached path is wiki/Home.md ==="

echo "--- 1a: normalizeWikiCachePath unit tests (Home.md -> wiki/Home.md, subdirectories, edge cases) ---"
run_go_test "scenario-1a-normalize-unit" "./internal/service" '^TestNormalizeWikiCachePath$'

echo ""
echo "--- 1b: End-to-end sync with Slug: Home.md -> cached Record.Path == wiki/Home.md ---"
run_go_test "scenario-1b-sync-e2e" "./internal/service" '^TestWikiPathNormalizationInSync$'

echo ""
echo "=== scenario-2: Existing test fixtures updated, go test passes ==="

echo "--- 2a: internal/gitcode/... tests pass ---"
go test ./internal/gitcode/... -count=1 -v 2>&1 | grep -E "PASS|FAIL|ok"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: go test ./internal/gitcode/... returned non-zero exit code"
  exit 1
fi

echo ""
echo "--- 2b: internal/service/... tests pass ---"
go test ./internal/service/... -count=1 -v 2>&1 | grep -E "PASS|FAIL|ok"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: go test ./internal/service/... returned non-zero exit code"
  exit 1
fi

echo ""
echo "--- 2c: full repository go test ./... pass ---"
go test ./... 2>&1 | grep -E "^ok|^FAIL"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: go test ./... returned non-zero exit code"
  exit 1
fi

echo ""
echo "--- 2d: git diff --check ---"
git diff --check
echo "[design-package:014] git diff --check passed (no whitespace violations)"

echo ""
echo "[design-package:014] PASS"
exit 0
