#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/gitcode-mcp"
CACHE_PATH="$WORK/cache/cache.db"
PRIVATE_PATH_SENTINEL="$WORK/private/root/should-not-leak"
TOKEN_SECRET="validation-token-secret-002"
USERINFO_SECRET="validation-userinfo-secret-002"
QUERY_SECRET="validation-query-secret-002"

export GOCACHE="$WORK/go-build-cache"
export GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"

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
  printf '%s' "$code" >"$WORK/$name.code"
}

code_of() {
  cat "$WORK/$1.code"
}

out_of() {
  cat "$WORK/$1.out"
}

err_of() {
  cat "$WORK/$1.err"
}

assert_code() {
  local name="$1"
  local want="$2"
  local got
  got="$(code_of "$name")"
  [[ "$got" == "$want" ]] || fail "$name exit code got $got want $want; stdout=$(out_of "$name"); stderr=$(err_of "$name")"
}

assert_nonzero() {
  local name="$1"
  local got
  got="$(code_of "$name")"
  [[ "$got" != "0" ]] || fail "$name unexpectedly succeeded; stdout=$(out_of "$name"); stderr=$(err_of "$name")"
}

assert_contains() {
  local name="$1"
  local needle="$2"
  if ! grep -Fq "$needle" "$WORK/$name.out" "$WORK/$name.err"; then
    fail "$name missing expected text: $needle; stdout=$(out_of "$name"); stderr=$(err_of "$name")"
  fi
}

assert_stdout_contains() {
  local name="$1"
  local needle="$2"
  if ! grep -Fq "$needle" "$WORK/$name.out"; then
    fail "$name stdout missing expected text: $needle; stdout=$(out_of "$name"); stderr=$(err_of "$name")"
  fi
}

assert_all_outputs_not_contains() {
  local needle="$1"
  if grep -Fq "$needle" "$WORK"/*.out "$WORK"/*.err 2>/dev/null; then
    fail "forbidden text leaked: $needle"
  fi
}

assert_status_fixture_b_absent() {
  run_capture status-fixture-b env -i \
    HOME="$WORK/home" \
    XDG_CONFIG_HOME="$WORK/xdg-config" \
    XDG_CACHE_HOME="$WORK/xdg-cache" \
    GITCODE_TOKEN="$TOKEN_SECRET" \
    PATH="$PATH" \
    "$BIN" --cache-path "$CACHE_PATH" repo status --repo fixture-b
  assert_code status-fixture-b 3
  assert_contains status-fixture-b "not found"
}

cd "$ROOT"
go test ./internal/cache ./internal/service ./internal/cli
go build -o "$BIN" ./cmd/gitcode-mcp

mkdir -p "$(dirname "$CACHE_PATH")" "$WORK/home" "$WORK/xdg-config" "$WORK/xdg-cache" "$PRIVATE_PATH_SENTINEL"

run_capture add-fixture-a env -i \
  HOME="$WORK/home" \
  XDG_CONFIG_HOME="$WORK/xdg-config" \
  XDG_CACHE_HOME="$WORK/xdg-cache" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" --cache-path "$CACHE_PATH" repo add \
  --repo fixture-a \
  --owner owner-a \
  --name repo-a \
  --api-base-url "https://example.invalid/api" \
  --scopes issues,wiki \
  --alias proj
assert_code add-fixture-a 0

run_capture status-fixture-a env -i \
  HOME="$WORK/home" \
  XDG_CONFIG_HOME="$WORK/xdg-config" \
  XDG_CACHE_HOME="$WORK/xdg-cache" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" --cache-path "$CACHE_PATH" repo status --repo fixture-a
assert_code status-fixture-a 0
for want in \
  "repo_id: fixture-a" \
  "owner: owner-a" \
  "name: repo-a" \
  "api_base_url: https://example.invalid/api" \
  "scopes: issues,wiki" \
  "aliases: proj" \
  "binding_state: ready" \
  "alias_conflict_state: none" \
  "cache_state:" \
  "index_state:"; do
  assert_stdout_contains status-fixture-a "$want"
done

run_capture add-credential-url env -i \
  HOME="$WORK/home" \
  XDG_CONFIG_HOME="$WORK/xdg-config" \
  XDG_CACHE_HOME="$WORK/xdg-cache" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" --cache-path "$CACHE_PATH" repo add \
  --repo fixture-redacted \
  --owner owner-redacted \
  --name repo-redacted \
  --api-base-url "https://user:${USERINFO_SECRET}@example.invalid/api?access_token=${QUERY_SECRET}&safe=1" \
  --scopes issues \
  --alias redacted-proj
assert_code add-credential-url 0

run_capture status-redacted env -i \
  HOME="$WORK/home" \
  XDG_CONFIG_HOME="$WORK/xdg-config" \
  XDG_CACHE_HOME="$WORK/xdg-cache" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" --cache-path "$CACHE_PATH" repo status --repo fixture-redacted
assert_code status-redacted 0
assert_stdout_contains status-redacted "api_base_url: https://example.invalid/api?safe=1"

run_capture duplicate-repo env -i \
  HOME="$WORK/home" \
  XDG_CONFIG_HOME="$WORK/xdg-config" \
  XDG_CACHE_HOME="$WORK/xdg-cache" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" --cache-path "$CACHE_PATH" repo add \
  --repo fixture-a \
  --owner owner-changed \
  --name repo-changed \
  --api-base-url "https://example.invalid/changed" \
  --scopes issues
assert_nonzero duplicate-repo
assert_contains duplicate-repo "conflict"

run_capture alias-collision env -i \
  HOME="$WORK/home" \
  XDG_CONFIG_HOME="$WORK/xdg-config" \
  XDG_CACHE_HOME="$WORK/xdg-cache" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" --cache-path "$CACHE_PATH" repo add \
  --repo fixture-b \
  --owner owner-b \
  --name repo-b \
  --api-base-url "https://example.invalid/api" \
  --scopes issues,wiki \
  --alias proj
assert_nonzero alias-collision
assert_contains alias-collision "conflict"
assert_status_fixture_b_absent

run_capture status-fixture-a-after-conflicts env -i \
  HOME="$WORK/home" \
  XDG_CONFIG_HOME="$WORK/xdg-config" \
  XDG_CACHE_HOME="$WORK/xdg-cache" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" --cache-path "$CACHE_PATH" repo status --repo fixture-a
assert_code status-fixture-a-after-conflicts 0
assert_stdout_contains status-fixture-a-after-conflicts "owner: owner-a"
assert_stdout_contains status-fixture-a-after-conflicts "name: repo-a"
assert_stdout_contains status-fixture-a-after-conflicts "aliases: proj"

run_capture missing-repo env -i \
  HOME="$WORK/home" \
  XDG_CONFIG_HOME="$WORK/xdg-config" \
  XDG_CACHE_HOME="$WORK/xdg-cache" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" --cache-path "$CACHE_PATH" repo status --repo missing-repo
assert_code missing-repo 3
assert_contains missing-repo "repository"
assert_contains missing-repo "not found"
assert_contains missing-repo "failure_class"

assert_all_outputs_not_contains "$TOKEN_SECRET"
assert_all_outputs_not_contains "$USERINFO_SECRET"
assert_all_outputs_not_contains "$QUERY_SECRET"
assert_all_outputs_not_contains "$PRIVATE_PATH_SENTINEL"
assert_all_outputs_not_contains "user:${USERINFO_SECRET}"
assert_all_outputs_not_contains "access_token=${QUERY_SECRET}"

printf 'PASS: repo registry add validation scenarios passed\n'
