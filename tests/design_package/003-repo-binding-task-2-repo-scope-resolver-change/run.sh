#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/003-repo-binding-task-2-repo-scope-resolver-change"
WORK="$SCENARIO_DIR/.tmp-run"
LOCK_ARTIFACT="$ROOT/internal/service/gitcode-mcp-sync.lock"
rm -rf "$WORK"
mkdir -p "$WORK"
cleanup() {
  rm -f "$LOCK_ARTIFACT"
  rm -rf "$WORK"
}
trap cleanup EXIT

export GOCACHE="$WORK/go-build-cache"
export GOTMPDIR="$WORK/go-tmp"
export TMPDIR="$WORK/tmp"
export GITCODE_LIVE_TEST=0
mkdir -p "$GOCACHE" "$GOTMPDIR" "$TMPDIR"
initial_non_scenario_status="$(git -C "$ROOT" status --short --untracked-files=all | grep -Fv ' tests/design_package/003-repo-binding-task-2-repo-scope-resolver-change/' || true)"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

run_go_test() {
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

assert_output_contains() {
  local name="$1"
  local needle="$2"
  if ! grep -Fq -- "$needle" "$WORK/$name.out"; then
    fail "$name did not run expected evidence marker: $needle"
  fi
}

cd "$ROOT"

run_go_test cli-scope go test ./internal/cli -run 'TestCLIRepoScopedDuplicateAlias' -count=1 -v
assert_output_contains cli-scope 'TestCLIRepoScopedDuplicateAlias'

run_go_test service-scope go test ./internal/service -run 'TestRepoScopedAliasResolution|TestDisabledWikiScopeRejectedBeforeClient|TestSyncRejectsDisabledWikiScopeBeforeAdapter|TestBuildAdapterRouteValidatesRepoScope' -count=1 -v
assert_output_contains service-scope 'TestRepoScopedAliasResolution'
assert_output_contains service-scope 'TestDisabledWikiScopeRejectedBeforeClient'
assert_output_contains service-scope 'TestSyncRejectsDisabledWikiScopeBeforeAdapter'
assert_output_contains service-scope 'TestBuildAdapterRouteValidatesRepoScope'

run_go_test mcp-scope go test ./internal/mcp -run 'TestMCPRepoScopedDuplicateAlias' -count=1 -v
assert_output_contains mcp-scope 'TestMCPRepoScopedDuplicateAlias'

run_go_test required-offline-suite go test ./...
run_go_test diff-check git diff --check
rm -f "$LOCK_ARTIFACT"
rm -rf "$WORK"

status_output="$(git status --short --untracked-files=all)"
non_scenario_status="$(printf '%s\n' "$status_output" | grep -Fv ' tests/design_package/003-repo-binding-task-2-repo-scope-resolver-change/' || true)"
if [[ "$non_scenario_status" != "$initial_non_scenario_status" ]]; then
  printf '%s\n' "$status_output" >&2
  fail 'validation modified files outside scenario directory'
fi

printf 'PASS: repo scope resolver validation scenarios passed offline\n'
