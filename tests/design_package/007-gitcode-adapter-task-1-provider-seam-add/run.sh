#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/007-gitcode-adapter-task-1-provider-seam-add"
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

run_capture fixture-contract go test ./internal/gitcode ./internal/testnet -run 'TestFixtureProviderContract|TestFixtureProviderScenarios|TestProviderPaginationGuards|TestNoExternalNetwork' -count=1 -v
assert_log_contains fixture-contract "TestFixtureProviderContract"
assert_log_contains fixture-contract "TestFixtureProviderScenarios"
assert_log_contains fixture-contract "TestProviderPaginationGuards"
assert_log_contains fixture-contract "TestNoExternalNetwork"

run_capture live-admission go test ./internal/gitcode -run 'TestLiveProviderAdmission' -count=1 -v
assert_log_contains live-admission "TestLiveProviderAdmission"

run_capture live-gate-redaction go test ./internal/gitcode ./internal/testnet -run 'TestRequireLiveProviderForTestGate|TestIntegrationRequireLiveIntegration|TestRedactedCapture|TestSanitizedFixtures' -count=1 -v
assert_log_contains live-gate-redaction "TestRequireLiveProviderForTestGate"
assert_log_contains live-gate-redaction "TestIntegrationRequireLiveIntegration"
assert_log_contains live-gate-redaction "TestRedactedCapture"
assert_log_contains live-gate-redaction "TestSanitizedFixtures"

run_capture package-contract go test ./internal/gitcode ./internal/testnet -count=1
run_capture all-tests go test ./...
run_capture diff-check git diff --check

printf 'PASS: scenario-007 provider seam validation passed\n'
