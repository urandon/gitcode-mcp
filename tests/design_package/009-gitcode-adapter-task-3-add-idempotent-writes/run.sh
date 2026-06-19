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

run_go_test "009-gitcode-adapter-task-3-add-idempotent-writes-scenario-1" '^TestWriteIdempotency$/^sends idempotency key and JSON payload$'
run_go_test "009-gitcode-adapter-task-3-add-idempotent-writes-scenario-2" '^TestWriteIdempotency$/^conflict returns local and remote payloads$'
run_go_test "009-gitcode-adapter-task-3-add-idempotent-writes-scenario-3" '^TestWriteIdempotency$/^retry preserves key and replay option$'
run_go_test "009-gitcode-adapter-task-3-add-idempotent-writes-scenario-4" '^TestWriteUsesEndpointBuilders$'
run_go_test "regression: idempotent write compatibility" '^(TestWriteIdempotency|TestWriteUsesEndpointBuilders|TestWriteEndpointsTemplate|TestReadRetry|TestFailureModes)$'
