#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/021-internal-cache-task-2-add-schema-version-check-against-version-11-inter"
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

# Scenario 1: Cache file with schema_version > 11: open returns schema_incompatible diagnostic.

# 1a: CheckVersionCompatibility for future version
run_capture cache-compat-future go test ./internal/cache -run 'TestCheckVersionCompatibilityFuture' -count=1 -v
assert_log_contains cache-compat-future "TestCheckVersionCompatibilityFuture"

# 1b: NewSQLiteStore rejects future schema version
run_capture cache-store-future go test ./internal/cache -run 'TestNewSQLiteStoreFutureSchemaBlocked' -count=1 -v
assert_log_contains cache-store-future "TestNewSQLiteStoreFutureSchemaBlocked"

# 1c: NewSQLiteReadOnlyStore rejects future schema version
run_capture cache-ro-future go test ./internal/cache -run 'TestReadOnlyStoreRejectsFutureVersion' -count=1 -v
assert_log_contains cache-ro-future "TestReadOnlyStoreRejectsFutureVersion"

# 1d: StartupDiagnosticFromError maps SchemaVersionError to schema_incompatible
run_capture mcp-schema-diag go test ./internal/mcp -run 'TestStartupDiagnosticSchemaIncompatible' -count=1 -v
assert_log_contains mcp-schema-diag "TestStartupDiagnosticSchemaIncompatible"

# 1e: End-to-end MCP server with schema version 99 returns schema_incompatible in tools/list and doctor
run_capture e2e-schema-99 go test ./cmd/gitcode-mcp -run 'TestMainMCPReadinessModeSchemaIncompatible' -count=1 -v
assert_log_contains e2e-schema-99 "PASS"

# Scenario 2: Cache file with schema_version 11: opens normally.

# 2a: CheckVersionCompatibility for current version
run_capture cache-compat-current go test ./internal/cache -run 'TestCheckVersionCompatibilityCurrent' -count=1 -v
assert_log_contains cache-compat-current "TestCheckVersionCompatibilityCurrent"

# 2b: TestSchemaVersion confirms currentSchemaVersion == 11 and migration idempotency
run_capture cache-schema-ver go test ./internal/cache -run 'TestSchemaVersion' -count=1 -v
assert_log_contains cache-schema-ver "TestSchemaVersion"

# Scenario 3: Cache file with version < 11: existing migration path used. currentSchemaVersion constant is 11.

# 3a: CheckVersionCompatibility for version 2 suggests migration
run_capture cache-compat-v2 go test ./internal/cache -run 'TestCheckVersionCompatibilityVersionTwoSuggestsMigration' -count=1 -v
assert_log_contains cache-compat-v2 "TestCheckVersionCompatibilityVersionTwoSuggestsMigration"

# 3b: NewSQLiteStore with version 2 returns ErrSchemaVersionIncompatible with migrate hint
run_capture cache-store-v2 go test ./internal/cache -run 'TestNewSQLiteStoreVersionTwoBlockedWithMigrateHint' -count=1 -v
assert_log_contains cache-store-v2 "TestNewSQLiteStoreVersionTwoBlockedWithMigrateHint"

# 3c: NewSQLiteReadOnlyStore accepts version 2 (Compatible == true)
run_capture cache-ro-v2 go test ./internal/cache -run 'TestReadOnlyStoreAcceptsVersionTwo' -count=1 -v
assert_log_contains cache-ro-v2 "TestReadOnlyStoreAcceptsVersionTwo"

# 3d: Migration from version 2 to 4 works
run_capture cache-migrate-v2v4 go test ./internal/cache -run 'TestMigrateFromVersion2ToVersion4' -count=1 -v
assert_log_contains cache-migrate-v2v4 "TestMigrateFromVersion2ToVersion4"

# 3e: Migration from current version is a no-op
run_capture cache-migrate-noop go test ./internal/cache -run 'TestMigrateFromCurrentVersionNoOp' -count=1 -v
assert_log_contains cache-migrate-noop "TestMigrateFromCurrentVersionNoOp"

# 3f: TestSchemaVersion assert confirms currentSchemaVersion == 11 (same test also used for Scenario 2)
run_capture cache-schema-ver-3 go test ./internal/cache -run 'TestSchemaVersion' -count=1 -v
assert_log_contains cache-schema-ver-3 "TestSchemaVersion"

# Full cache test suite pass
run_capture cache-full go test ./internal/cache/... -count=1

# Full repository test suite
run_capture all-tests go test ./...

# Whitespace check
run_capture diff-check git diff --check

printf 'PASS: all schema version check scenarios verified\n'
