#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

export GONOSUMDB="${GONOSUMDB:-}"
export GOPRIVATE="${GOPRIVATE:-}"

run_test() {
  local name="$1"
  local pattern="$2"
  printf '==> %s\n' "$name"
  go test ./internal/gitcode/... -run "$pattern" -count=1
}

run_test "007 scenario 1: GetIssue contract" '^TestContract$'
run_test "007 scenario 2: attachment contract" '^TestAttachmentContract$'
run_test "007 scenario 3: read retry behavior" '^TestReadRetry$'
run_test "007 scenario 4: timeout and failure modes" '^(TestTimeout|TestFailureModes)$'
