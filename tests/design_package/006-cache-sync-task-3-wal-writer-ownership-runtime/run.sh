#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/006-cache-sync-task-3-wal-writer-ownership-runtime"
WORK="$SCENARIO_DIR/.tmp-run"
LOCK_ARTIFACT="$ROOT/internal/service/gitcode-mcp-sync.lock"

if [[ -d "$WORK" ]]; then
  chmod -R u+w "$WORK" 2>/dev/null || true
  rm -rf "$WORK"
fi
mkdir -p "$WORK/go-build-cache" "$WORK/go-tmp" "$WORK/tmp" "$WORK/home"

cleanup() {
  rm -f "$LOCK_ARTIFACT"
  if [[ -d "$WORK" ]]; then
    chmod -R u+w "$WORK" 2>/dev/null || true
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT

export GOCACHE="$WORK/go-build-cache"
export GOPATH="$WORK/go-path"
export GOMODCACHE="$WORK/go-mod-cache"
export GOTMPDIR="$WORK/go-tmp"
export TMPDIR="$WORK/tmp"
export HOME="$WORK/home"
export GITCODE_LIVE_TEST=0
export GITCODE_TOKEN=""
rm -f "$LOCK_ARTIFACT"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

run_capture() {
  local name="$1"
  shift
  set +e
  "$@" >"$WORK/$name.out" 2>"$WORK/$name.err"
  local code=$?
  set -e
  if [[ "$code" != "0" ]]; then
    printf '%s\n' "--- $name stdout ---" >&2
    cat "$WORK/$name.out" >&2
    printf '%s\n' "--- $name stderr ---" >&2
    cat "$WORK/$name.err" >&2
    fail "$name exited $code"
  fi
}

assert_log_contains() {
  local name="$1"
  local needle="$2"
  if ! grep -Fq -- "$needle" "$WORK/$name.out" && ! grep -Fq -- "$needle" "$WORK/$name.err"; then
    fail "$name did not emit expected evidence marker: $needle"
  fi
}

run_capture cache-runtime go test ./internal/cache -run 'TestWriterAdmissionWALOwnershipRuntime|TestCheckpointAfterWriteHeavySync|TestLockContention' -count=1 -v
assert_log_contains cache-runtime "TestWriterAdmissionWALOwnershipRuntime"
assert_log_contains cache-runtime "TestCheckpointAfterWriteHeavySync"
assert_log_contains cache-runtime "TestLockContention"

run_capture service-runtime go test ./internal/service -run 'TestSyncLockContention' -count=1 -v
assert_log_contains service-runtime "TestSyncLockContention"

run_capture all-tests go test ./...
run_capture diff-check git diff --check

printf 'PASS: scenario-006 WAL writer ownership runtime validation passed\n'
