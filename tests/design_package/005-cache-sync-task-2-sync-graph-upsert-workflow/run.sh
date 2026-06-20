#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/005-cache-sync-task-2-sync-graph-upsert-workflow"
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

run_expect_success_or_product_failure() {
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
    fail "$name failed; sync product surface must support offline fixture-backed gitcode-mcp sync --repo <repo_id> --issues --wiki --index"
  fi
}

assert_output_contains() {
  local name="$1"
  local needle="$2"
  if ! grep -Fq -- "$needle" "$WORK/$name.out"; then
    fail "$name did not emit expected evidence marker: $needle"
  fi
}

assert_json_count_at_least() {
  local file="$1"
  local field="$2"
  local min="$3"
  python3 - "$file" "$field" "$min" <<'PY'
import json, sys
path, field, min_value = sys.argv[1], sys.argv[2], int(sys.argv[3])
with open(path, 'r', encoding='utf-8') as handle:
    data = json.load(handle)
value = data.get(field)
if not isinstance(value, int) or value < min_value:
    raise SystemExit(f'{field}={value!r}, want >= {min_value}')
PY
}

CACHE="$WORK/cache.db"

run_capture repo-add go run ./cmd/gitcode-mcp --cache-path "$CACHE" repo add --repo fixture-a --owner public-owner --name public-repo --api-base-url https://example.invalid/api --scopes issues,wiki
assert_output_contains repo-add "repo_id: fixture-a"

run_expect_success_or_product_failure sync-fixture go run ./cmd/gitcode-mcp --cache-path "$CACHE" sync --repo fixture-a --issues --wiki --index
assert_output_contains sync-fixture "succeeded"

run_capture search-offline go run ./cmd/gitcode-mcp --cache-path "$CACHE" search --repo fixture-a remote
assert_output_contains search-offline "ISSUE-42"
assert_output_contains search-offline "WIKI-HOME"

run_capture get-issue-offline go run ./cmd/gitcode-mcp --cache-path "$CACHE" get --repo fixture-a ISSUE-42
assert_output_contains get-issue-offline "kind: issue"
assert_output_contains get-issue-offline "remote issue"

run_capture get-wiki-offline go run ./cmd/gitcode-mcp --cache-path "$CACHE" get --repo fixture-a WIKI-HOME
assert_output_contains get-wiki-offline "kind: wiki"
assert_output_contains get-wiki-offline "remote wiki"

run_capture snippet-offline go run ./cmd/gitcode-mcp --cache-path "$CACHE" get-snippet --repo fixture-a ISSUE-42 --line-start 1 --line-end 3
assert_output_contains snippet-offline "remote issue"

run_capture cache-status go run ./cmd/gitcode-mcp --cache-path "$CACHE" cache-status --repo fixture-a --format json
assert_json_count_at_least "$WORK/cache-status.out" records 2
assert_json_count_at_least "$WORK/cache-status.out" comments 1
assert_json_count_at_least "$WORK/cache-status.out" identity_aliases 2
assert_json_count_at_least "$WORK/cache-status.out" sync_events 2
assert_json_count_at_least "$WORK/cache-status.out" remote_revisions 2
assert_json_count_at_least "$WORK/cache-status.out" chunks 2

run_capture targeted-service-tests go test ./internal/service -run 'TestSyncGraphFixtureOfflineReadsIssueWikiCommentsAndChunks|TestSyncIdempotencyReplay' -count=1
run_capture targeted-cache-tests go test ./internal/cache -run 'TestUpsertSyncGraphIdempotentRepeat|TestUpsertSyncGraphProjectionThenRemotePreservesProjectionAliasBoundary' -count=1
run_capture all-tests go test ./...
run_capture diff-check git diff --check

printf 'PASS: scenario-005 sync graph upsert validation passed\n'
