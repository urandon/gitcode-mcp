#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/022-internal-cache-task-3-validate-cache-concurrency-and-lock-diagnostics-te"
WORK="$SCENARIO_DIR/.tmp-run"

if [[ -d "$WORK" ]]; then
  chmod -R u+w "$WORK" 2>/dev/null || true
  rm -rf "$WORK"
fi
mkdir -p "$WORK/go-build-cache" "$WORK/go-tmp" "$WORK/tmp" "$WORK/home" "$WORK/go-path" "$WORK/go-mod-cache"

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
    printf '%s\n' "--- $name stderr ---" >&2
    if [[ -f "$WORK/$name.err" ]]; then
      cat "$WORK/$name.err" >&2
    fi
    printf '%s\n' "--- $name stdout ---" >&2
    if [[ -f "$WORK/$name.out" ]]; then
      cat "$WORK/$name.out" >&2
    fi
    fail "$name exited $code"
  fi
}

assert_log_contains() {
  local name="$1"
  local needle="$2"
  if ! grep -Fq -- "$needle" "$WORK/$name.out" 2>/dev/null && ! grep -Fq -- "$needle" "$WORK/$name.err" 2>/dev/null; then
    fail "$name did not emit expected evidence marker: $needle"
  fi
}

cleanup() {
  if [[ -d "$WORK" ]]; then
    chmod -R u+w "$WORK" 2>/dev/null || true
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT

# Deterministic pre-flight: verify Go toolchain and pre-fetch module dependencies
# into the isolated GOMODCACHE so that transient module resolution failures
# surface as explicit run_capture failures rather than Go exit code 65
# (os.PathError / package not found in GOROOT-like scenarios).
if ! command -v go >/dev/null 2>&1; then
  fail "go not found on PATH"
fi

run_capture cache-mod-download go mod download

# Scenario 1: go test ./internal/cache/... passes.
run_capture cache-full go test -timeout 60s ./internal/cache/... -count=1

# Scenario 2: All concurrency scenarios verified with actual WAL-mode SQLite.

# SCN-022-01: Two concurrent search goroutines, no internal_error
run_capture cache-search-concurrent go test -timeout 30s ./internal/cache -run 'TestConcurrentSearchSources' -count=1 -v
assert_log_contains cache-search-concurrent "TestConcurrentSearchSources"

# SCN-022-02: Writer hold, concurrent readers complete via independent connections
run_capture cache-writer-hold-readers go test -timeout 30s ./internal/cache -run 'TestWriterHoldReadersUnblocked' -count=1 -v
assert_log_contains cache-writer-hold-readers "TestWriterHoldReadersUnblocked"

# SCN-022-03: Two concurrent writers - second returns cache_busy
run_capture cache-two-writers go test -timeout 30s ./internal/cache -run 'TestTwoWritersContentionCacheBusy' -count=1 -v
assert_log_contains cache-two-writers "TestTwoWritersContentionCacheBusy"

# SCN-022-04: Three goroutines (2 readers + 1 writer) - readers complete, writer gets cache_busy
run_capture cache-three-routines go test -timeout 30s ./internal/cache -run 'TestThreeRoutinesTwoReadersOneWriter' -count=1 -v
assert_log_contains cache-three-routines "TestThreeRoutinesTwoReadersOneWriter"

# SCN-022-05: Future schema version yields schema_incompatible
run_capture cache-future-schema go test -timeout 30s ./internal/cache -run 'TestFutureSchemaIncompatibleDiagnostic' -count=1 -v
assert_log_contains cache-future-schema "TestFutureSchemaIncompatibleDiagnostic"

# Writer admission WAL ownership
run_capture cache-wal-ownership go test -timeout 30s ./internal/cache -run 'TestWriterAdmissionWALOwnershipRuntime' -count=1 -v
assert_log_contains cache-wal-ownership "TestWriterAdmissionWALOwnershipRuntime"

# Three readers one writer concurrency (store_test.go)
run_capture cache-three-readers go test -timeout 30s ./internal/cache -run 'TestThreeReadersOneWriterConcurrency' -count=1 -v
assert_log_contains cache-three-readers "TestThreeReadersOneWriterConcurrency"

# Scenario 3: Lock contention diagnostics are typed cache_busy.

# CacheBusyDiagnosticCodeOnLockContention directly asserts DiagnosticCode() == "cache_busy"
run_capture cache-busy-diag go test -timeout 30s ./internal/cache -run 'TestCacheBusyDiagnosticCodeOnLockContention' -count=1 -v
assert_log_contains cache-busy-diag "TestCacheBusyDiagnosticCodeOnLockContention"

# LockContention tests prove no internal_error (only *ErrLockContention)
run_capture cache-lock-contention go test -timeout 30s ./internal/cache -run 'TestLockContention' -count=1 -v
assert_log_contains cache-lock-contention "TestLockContention"

# Lock contention blocks simulated sync - proves cache_busy blocking behavior
run_capture cache-lock-sync go test -timeout 30s ./internal/cache -run 'TestLockContentionBlocksSimulatedSync' -count=1 -v
assert_log_contains cache-lock-sync "TestLockContentionBlocksSimulatedSync"

# Scenario 4: No internal_error for lock contention cases.
run_capture cache-no-internal-error go test -timeout 60s ./internal/cache -run 'TestLockContention|TestCacheBusyDiagnosticCodeOnLockContention|TestWriterAdmissionWALOwnershipRuntime|TestLockContentionBlocksSimulatedSync|TestThreeReadersOneWriterConcurrency|TestConcurrentSearchSources|TestTwoWritersContentionCacheBusy|TestThreeRoutinesTwoReadersOneWriter|TestWriterHoldReadersUnblocked' -count=1 -v

# Race detector on cache package
run_capture cache-race go test -timeout 120s ./internal/cache/... -race -count=1

# Whitespace check
run_capture diff-check git diff --check

printf 'PASS: all cache concurrency and lock diagnostics scenarios verified\n'
