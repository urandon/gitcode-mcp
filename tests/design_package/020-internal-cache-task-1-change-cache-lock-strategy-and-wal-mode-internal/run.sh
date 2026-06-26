#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/020-internal-cache-task-1-change-cache-lock-strategy-and-wal-mode-internal"
WORK="$SCENARIO_DIR/.tmp-run"

if [[ -d "$WORK" ]]; then
  chmod -R u+w "$WORK" 2>/dev/null || true
  rm -rf "$WORK"
fi
mkdir -p "$WORK/go-build-cache" "$WORK/go-tmp" "$WORK/tmp" "$WORK/home"

cleanup() {
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

# Scenario 1 & 2: Two concurrent reads and writer hold with concurrent readers
run_capture cache-wal-readers go test ./internal/cache -run 'TestWriterAdmissionWALOwnershipRuntime' -count=1 -v
assert_log_contains cache-wal-readers "TestWriterAdmissionWALOwnershipRuntime"

# Scenario 3: Two concurrent writers, one returns cache_busy not internal_error
run_capture cache-cache-busy go test ./internal/cache -run 'TestCacheBusyDiagnosticCodeOnLockContention' -count=1 -v
assert_log_contains cache-cache-busy "TestCacheBusyDiagnosticCodeOnLockContention"

# Scenario 3: Diagnostic classifier verification
run_capture diagnostics-cache-busy go test ./internal/diagnostics -run 'TestClassifierCacheBusy' -count=1 -v
assert_log_contains diagnostics-cache-busy "TestClassifierCacheBusy"

# Scenario 3: MCP error mapping verification
run_capture mcp-cache-busy go test ./internal/mcp -run 'TestMCPRuntimeLockContentionErrorMapping' -count=1 -v
assert_log_contains mcp-cache-busy "TestMCPRuntimeLockContentionErrorMapping"

# Scenario 4: Three goroutines (2 readers + 1 writer)
run_capture cache-three-goroutines go test ./internal/cache -run 'TestThreeReadersOneWriterConcurrency' -count=1 -v
assert_log_contains cache-three-goroutines "TestThreeReadersOneWriterConcurrency"

# Scenario 4: Full cache test suite pass
run_capture cache-full go test ./internal/cache/... -count=1

# Startup diagnostic for lock contention
run_capture mcp-startup-lock go test ./internal/mcp -run 'TestStartupDiagnosticCacheLockContention' -count=1 -v
assert_log_contains mcp-startup-lock "TestStartupDiagnosticCacheLockContention"

# SSEReadiness lock contention 
run_capture mcp-sse-lock go test ./internal/mcp -run 'TestHTTPSSEReadinessLockContention' -count=1 -v
assert_log_contains mcp-sse-lock "TestHTTPSSEReadinessLockContention"

# Full repository test suite
run_capture all-tests go test ./...

# Whitespace check
run_capture diff-check git diff --check

printf 'PASS: all lock strategy and WAL mode scenarios verified\n'
