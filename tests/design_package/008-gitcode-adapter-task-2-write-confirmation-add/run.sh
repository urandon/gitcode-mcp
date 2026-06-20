#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/008-gitcode-adapter-task-2-write-confirmation-add"
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
export GITCODE_LIVE_TEST=""
export GITCODE_LIVE_TOKEN=""
export GITCODE_TEST_TOKEN=""
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

mkdir -p "internal/gitcode/product_selection_guard_tmp"
cleanup_guard() {
  rm -rf "internal/gitcode/product_selection_guard_tmp"
}
trap 'cleanup_guard; cleanup' EXIT

cat >"internal/gitcode/product_selection_guard_tmp/main.go" <<'GOGO'
package main

import (
	"gitcode-mcp/internal/gitcode"
)

func main() {
	allowedModes := []gitcode.ProviderMode{
		gitcode.ProviderModeLive,
		gitcode.ProviderModeFixture,
		gitcode.ProviderModeUnavailable,
	}
	if len(allowedModes) != 3 {
		panic("unexpected provider mode count")
	}
	_, _ = gitcode.NewLiveProvider(gitcode.ProviderConfig{Mode: gitcode.ProviderModeLive})
	_, _ = gitcode.NewFixtureProvider(gitcode.FixtureConfig{})
	_ = gitcode.NewUnavailableProvider("validation")
}
GOGO

run_capture confirmed-writes go test ./internal/gitcode -run 'TestConfirmedWriteOperations|TestWriteIdempotency|TestWriteUsesEndpointBuilders' -count=1 -v
assert_log_contains confirmed-writes "TestConfirmedWriteOperations"
assert_log_contains confirmed-writes "write-confirm-create-issue"
assert_log_contains confirmed-writes "write-confirm-update-issue"
assert_log_contains confirmed-writes "write-confirm-create-comment"
assert_log_contains confirmed-writes "write-confirm-create-wiki"
assert_log_contains confirmed-writes "write-confirm-update-wiki"
assert_log_contains confirmed-writes "TestWriteIdempotency"
assert_log_contains confirmed-writes "TestWriteUsesEndpointBuilders"

run_capture negative-writes go test ./internal/gitcode -run 'TestWriteNegativeScenariosDoNotConfirm|TestProviderWriteUnavailableDoesNotConfirm|TestWriteIdempotency/conflict' -count=1 -v
assert_log_contains negative-writes "TestWriteNegativeScenariosDoNotConfirm"
assert_log_contains negative-writes "write-validation-failed"
assert_log_contains negative-writes "write-conflict-redacted"
assert_log_contains negative-writes "write-auth-expired"
assert_log_contains negative-writes "write-forbidden"
assert_log_contains negative-writes "write-rate-limited"
assert_log_contains negative-writes "write-network-unavailable"
assert_log_contains negative-writes "write-malformed-success"
assert_log_contains negative-writes "TestProviderWriteUnavailableDoesNotConfirm"

run_capture provider-selection go test ./internal/gitcode -run 'TestProviderWriteUnavailableDoesNotConfirm|TestLiveProviderAdmission|TestFixtureProviderContract' -count=1 -v
assert_log_contains provider-selection "TestProviderWriteUnavailableDoesNotConfirm"
assert_log_contains provider-selection "TestLiveProviderAdmission"
assert_log_contains provider-selection "TestFixtureProviderContract"

run_capture product-selection-guard go run ./internal/gitcode/product_selection_guard_tmp
run_capture package-contract go test ./internal/gitcode ./internal/testnet -count=1
run_capture all-tests go test ./...
run_capture diff-check git diff --check

printf 'PASS: scenario-008 write confirmation validation passed\n'
