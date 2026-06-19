#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/gitcode-mcp"
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

assert_code() {
  local name="$1"
  local want="$2"
  local got
  got="$(cat "$WORK/$name.code")"
  [[ "$got" == "$want" ]] || fail "$name exit code got $got want $want; stdout=$(cat "$WORK/$name.out"); stderr=$(cat "$WORK/$name.err")"
}

assert_contains() {
  local name="$1"
  local needle="$2"
  if ! grep -Fq "$needle" "$WORK/$name.out" "$WORK/$name.err"; then
    fail "$name missing expected text: $needle; stdout=$(cat "$WORK/$name.out"); stderr=$(cat "$WORK/$name.err")"
  fi
}

assert_not_contains_all_outputs() {
  local needle="$1"
  if grep -Fq "$needle" "$WORK"/*.out "$WORK"/*.err 2>/dev/null; then
    fail "forbidden text leaked: $needle"
  fi
}

cd "$ROOT"
go test ./internal/config ./internal/cli
go build -o "$BIN" ./cmd/gitcode-mcp

# Scenario 1: explicit config init/locate/show/auth status through real CLI.
EXPLICIT_HOME="$WORK/home-explicit"
EXPLICIT_XDG_CONFIG="$WORK/xdg-config-explicit"
EXPLICIT_XDG_CACHE="$WORK/xdg-cache-explicit"
EXPLICIT_CONFIG="$WORK/sanitized-explicit/config.yaml"
mkdir -p "$EXPLICIT_HOME" "$EXPLICIT_XDG_CONFIG" "$EXPLICIT_XDG_CACHE" "$(dirname "$EXPLICIT_CONFIG")"

run_capture init-explicit env -i \
  HOME="$EXPLICIT_HOME" \
  XDG_CONFIG_HOME="$EXPLICIT_XDG_CONFIG" \
  XDG_CACHE_HOME="$EXPLICIT_XDG_CACHE" \
  GITCODE_MCP_CONFIG="$EXPLICIT_CONFIG" \
  PATH="$PATH" \
  "$BIN" config init
assert_code init-explicit 0
assert_contains init-explicit "config_path: $EXPLICIT_CONFIG"
assert_contains init-explicit "config_format: yaml"
[[ -f "$EXPLICIT_CONFIG" ]] || fail "config init did not write YAML config"
[[ ! -e "${EXPLICIT_CONFIG%.yaml}.json" ]] || fail "config init wrote JSON sibling"

run_capture init-overwrite-refused env -i \
  HOME="$EXPLICIT_HOME" \
  XDG_CONFIG_HOME="$EXPLICIT_XDG_CONFIG" \
  XDG_CACHE_HOME="$EXPLICIT_XDG_CACHE" \
  GITCODE_MCP_CONFIG="$EXPLICIT_CONFIG" \
  PATH="$PATH" \
  "$BIN" config init
assert_code init-overwrite-refused 1

run_capture locate-explicit env -i \
  HOME="$EXPLICIT_HOME" \
  XDG_CONFIG_HOME="$EXPLICIT_XDG_CONFIG" \
  XDG_CACHE_HOME="$EXPLICIT_XDG_CACHE" \
  GITCODE_MCP_CONFIG="$EXPLICIT_CONFIG" \
  PATH="$PATH" \
  "$BIN" config locate
assert_code locate-explicit 0
assert_contains locate-explicit "config_path: $EXPLICIT_CONFIG"
assert_contains locate-explicit "config_source: explicit-yaml"
assert_contains locate-explicit "config_format: yaml"
assert_contains locate-explicit "config_exists: true"

FILE_SECRET="file-contained-secret-validation"
TOKEN_SECRET="validation-token-secret"
RAW_PROVIDER_DIAGNOSTIC="raw dbus validation failure details"
CACHE_OVERRIDE="$WORK/sanitized-cache-override"
cat >"$EXPLICIT_CONFIG" <<EOF_CONFIG
cache_path: $WORK/file-cache.db
gitcode_base_url: $FILE_SECRET
credential:
  store: env
EOF_CONFIG

run_capture show-redacted env -i \
  HOME="$EXPLICIT_HOME" \
  XDG_CONFIG_HOME="$EXPLICIT_XDG_CONFIG" \
  XDG_CACHE_HOME="$EXPLICIT_XDG_CACHE" \
  GITCODE_MCP_CONFIG="$EXPLICIT_CONFIG" \
  GITCODE_MCP_CACHE_DIR="$CACHE_OVERRIDE" \
  GITCODE_API_URL="https://api.example.invalid" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" config show --redacted
assert_code show-redacted 0
assert_contains show-redacted "config_source: explicit-yaml"
assert_contains show-redacted "cache_path: $CACHE_OVERRIDE/cache.db"
assert_contains show-redacted "cache_path_source: env:GITCODE_MCP_CACHE_DIR"
assert_contains show-redacted "gitcode_base_url_source: env:GITCODE_API_URL"
assert_contains show-redacted "credential_store_mode: env"
assert_contains show-redacted "credential_source: env:GITCODE_TOKEN"
assert_contains show-redacted "token_present: true"
assert_contains show-redacted "field_source.cache_path: env:GITCODE_MCP_CACHE_DIR"
assert_contains show-redacted "field_source.gitcode_base_url: env:GITCODE_API_URL"

run_capture auth-env-present env -i \
  HOME="$EXPLICIT_HOME" \
  XDG_CONFIG_HOME="$EXPLICIT_XDG_CONFIG" \
  XDG_CACHE_HOME="$EXPLICIT_XDG_CACHE" \
  GITCODE_MCP_CONFIG="$EXPLICIT_CONFIG" \
  GITCODE_TOKEN="$TOKEN_SECRET" \
  PATH="$PATH" \
  "$BIN" auth status
assert_code auth-env-present 0
assert_contains auth-env-present "credential_source: env:GITCODE_TOKEN"
assert_contains auth-env-present "token_present: true"
assert_contains auth-env-present "credential_store_mode: env"

run_capture auth-missing-env-only env -i \
  HOME="$EXPLICIT_HOME" \
  XDG_CONFIG_HOME="$EXPLICIT_XDG_CONFIG" \
  XDG_CACHE_HOME="$EXPLICIT_XDG_CACHE" \
  GITCODE_MCP_CONFIG="$EXPLICIT_CONFIG" \
  PATH="$PATH" \
  "$BIN" auth status
assert_code auth-missing-env-only 0
assert_contains auth-missing-env-only "credential_source: missing"
assert_contains auth-missing-env-only "token_present: false"
assert_contains auth-missing-env-only "credential_store_mode: env"
assert_contains auth-missing-env-only "credential_error_class: token-missing"
assert_contains auth-missing-env-only "remediation:"

# Scenario 2: default YAML path and legacy JSON compatibility through real CLI.
DEFAULT_HOME="$WORK/home-default"
DEFAULT_XDG_CONFIG="$WORK/xdg-config-default"
DEFAULT_XDG_CACHE="$WORK/xdg-cache-default"
DEFAULT_CONFIG="$DEFAULT_XDG_CONFIG/gitcode-mcp/config.yaml"
mkdir -p "$DEFAULT_HOME" "$DEFAULT_XDG_CONFIG" "$DEFAULT_XDG_CACHE"

run_capture init-default env -i \
  HOME="$DEFAULT_HOME" \
  XDG_CONFIG_HOME="$DEFAULT_XDG_CONFIG" \
  XDG_CACHE_HOME="$DEFAULT_XDG_CACHE" \
  PATH="$PATH" \
  "$BIN" config init
assert_code init-default 0
assert_contains init-default "config_path: $DEFAULT_CONFIG"
assert_contains init-default "config_format: yaml"
[[ -f "$DEFAULT_CONFIG" ]] || fail "default YAML config was not written"
[[ ! -e "$DEFAULT_XDG_CONFIG/gitcode-mcp/config.json" ]] || fail "default config init wrote JSON"

run_capture locate-default env -i \
  HOME="$DEFAULT_HOME" \
  XDG_CONFIG_HOME="$DEFAULT_XDG_CONFIG" \
  XDG_CACHE_HOME="$DEFAULT_XDG_CACHE" \
  PATH="$PATH" \
  "$BIN" config locate
assert_code locate-default 0
assert_contains locate-default "config_path: $DEFAULT_CONFIG"
assert_contains locate-default "config_source: default-yaml"
assert_contains locate-default "config_format: yaml"
assert_contains locate-default "config_exists: true"

LEGACY_HOME="$WORK/home-legacy"
LEGACY_XDG_CONFIG="$WORK/xdg-config-legacy"
LEGACY_XDG_CACHE="$WORK/xdg-cache-legacy"
LEGACY_CONFIG="$WORK/legacy/config.json"
mkdir -p "$LEGACY_HOME" "$LEGACY_XDG_CONFIG" "$LEGACY_XDG_CACHE" "$(dirname "$LEGACY_CONFIG")"
printf '{"cache_path":"%s","gitcode_base_url":"https://legacy.example.invalid"}\n' "$WORK/legacy-cache.db" >"$LEGACY_CONFIG"

run_capture locate-legacy env -i \
  HOME="$LEGACY_HOME" \
  XDG_CONFIG_HOME="$LEGACY_XDG_CONFIG" \
  XDG_CACHE_HOME="$LEGACY_XDG_CACHE" \
  GITCODE_CONFIG="$LEGACY_CONFIG" \
  PATH="$PATH" \
  "$BIN" config locate
assert_code locate-legacy 0
assert_contains locate-legacy "config_path: $LEGACY_CONFIG"
assert_contains locate-legacy "config_source: legacy-json"
assert_contains locate-legacy "config_format: json"
assert_contains locate-legacy "config_exists: true"

run_capture show-legacy env -i \
  HOME="$LEGACY_HOME" \
  XDG_CONFIG_HOME="$LEGACY_XDG_CONFIG" \
  XDG_CACHE_HOME="$LEGACY_XDG_CACHE" \
  GITCODE_CONFIG="$LEGACY_CONFIG" \
  PATH="$PATH" \
  "$BIN" config show --redacted
assert_code show-legacy 0
assert_contains show-legacy "config_source: legacy-json"
assert_contains show-legacy "config_format: json"
assert_contains show-legacy "cache_path_source: legacy-json"

# Negative leak invariants across captured command output.
assert_not_contains_all_outputs "$TOKEN_SECRET"
assert_not_contains_all_outputs "$FILE_SECRET"
assert_not_contains_all_outputs "$RAW_PROVIDER_DIAGNOSTIC"

printf 'PASS: config credential redacted UX validation scenarios passed\n'
