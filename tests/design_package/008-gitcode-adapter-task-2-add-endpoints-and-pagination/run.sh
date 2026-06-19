#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

export GONOSUMDB="${GONOSUMDB:-*}"

run_go_test() {
  local name="$1"
  local pattern="$2"
  printf '==> %s\n' "$name"
  go test ./internal/gitcode/... -run "$pattern" -count=1
}

run_go_test "008-gitcode-adapter-task-2-add-endpoints-and-pagination-scenario-1" '^TestEndpointsTemplate$'
run_go_test "008-gitcode-adapter-task-2-add-endpoints-and-pagination-scenario-2" '^TestAttachmentEndpointsTemplate$'
run_go_test "008-gitcode-adapter-task-2-add-endpoints-and-pagination-scenario-3" '^TestWriteEndpointsTemplate$'
run_go_test "008-gitcode-adapter-task-2-add-endpoints-and-pagination-scenario-4" '^TestPaginationSwappable$'
run_go_test "regression: endpoint pagination compatibility" '^(TestEndpointsTemplate|TestAttachmentEndpointsTemplate|TestWriteEndpointsTemplate|TestPaginationSwappable|TestPaginationLaterPageFailureReturnsNoRecords|TestContract|TestReadRetry)$'
