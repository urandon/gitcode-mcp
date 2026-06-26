#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

export GONOSUMDB="*"
export GOPRIVATE=""

run_go_test() {
  local name="$1"
  local pattern="$2"
  echo "[design-package:010] running ${name}: ${pattern}"
  go test ./internal/service -run "${pattern}" -count=1 -v
}

echo "=== scenario-1: empty_wiki diagnostic class (unbounded) ==="
run_go_test "scenario-1-unbounded" '^TestBulkSyncWikiEmptyWikiDiagnosticUnbounded$'

echo ""
echo "=== scenario-1: empty_wiki diagnostic class (bounded) ==="
run_go_test "scenario-1-bounded" '^TestBulkSyncWikiEmptyWikiDiagnosticBounded$'

echo ""
echo "=== scenario-2: remediation text references CLI init or UI step ==="
run_go_test "scenario-2-remediation-text" '^TestNormalizeSyncFailureMapsEmptyWiki$'

echo ""
echo "=== scenario-3: not classified as api_validation or provider_failure (unbounded) ==="
run_go_test "scenario-3-unbounded" '^TestBulkSyncWikiEmptyWikiDiagnosticUnbounded$'

echo ""
echo "=== scenario-3: not classified as api_validation or provider_failure (bounded) ==="
run_go_test "scenario-3-bounded" '^TestBulkSyncWikiEmptyWikiDiagnosticBounded$'

echo ""
echo "=== diagnostic code helper regression ==="
run_go_test "diagnostic-code-helper" '^TestErrorHasDiagnosticCode$'

echo ""
echo "[design-package:010] running focused empty-wiki regression set"
go test ./internal/service -run 'EmptyWiki|NormalizeSync|DiagnosticCode' -count=1

echo ""
echo "[design-package:010] running repository acceptance gate"
go test ./...
git diff --check
