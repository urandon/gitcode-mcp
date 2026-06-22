#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

DOC="$ROOT/docs/cache-and-sync-model.md"
SCHEMA="$ROOT/internal/cache/schema.go"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    fail "$label: missing '$needle'"
  fi
}

[[ -f "$DOC" ]] || fail "missing docs/cache-and-sync-model.md"
[[ -f "$SCHEMA" ]] || fail "missing internal/cache/schema.go"

DOC_TEXT="$(python3 - <<'PY' "$DOC"
import pathlib, sys
print(pathlib.Path(sys.argv[1]).read_text())
PY
)"

CURRENT_VERSION="$(python3 - <<'PY' "$SCHEMA"
import pathlib, re, sys
text = pathlib.Path(sys.argv[1]).read_text()
match = re.search(r'const\s+currentSchemaVersion\s*=\s*(\d+)', text)
if not match:
    raise SystemExit('currentSchemaVersion not found')
print(match.group(1))
PY
)"

assert_contains "$DOC_TEXT" "## Live Sync Semantics" "scenario-032-docs-cache-sync-policy-present"
assert_contains "$DOC_TEXT" "## Partial Failure Handling" "scenario-032-docs-cache-sync-policy-present"
assert_contains "$DOC_TEXT" "## Cache Migration" "scenario-032-docs-cache-sync-policy-present"
assert_contains "$DOC_TEXT" "schema version is \`$CURRENT_VERSION\`" "scenario-032-docs-schema-version-current"
assert_contains "$DOC_TEXT" "schema_version" "scenario-032-docs-schema-version-current"
assert_contains "$DOC_TEXT" "PRAGMA user_version" "scenario-032-docs-schema-version-current"
assert_contains "$DOC_TEXT" "PartialSyncError" "scenario-032-docs-cache-sync-policy-present"
assert_contains "$DOC_TEXT" "success_count" "scenario-032-docs-cache-sync-policy-present"
assert_contains "$DOC_TEXT" "failure_count" "scenario-032-docs-cache-sync-policy-present"
assert_contains "$DOC_TEXT" "zero_delta" "scenario-032-docs-sync-status-consistency"
assert_contains "$DOC_TEXT" "started_at" "scenario-032-docs-sync-status-consistency"
assert_contains "$DOC_TEXT" "completed_at" "scenario-032-docs-sync-status-consistency"
assert_contains "$DOC_TEXT" "remote_revision" "scenario-032-docs-sync-status-consistency"
assert_contains "$DOC_TEXT" "migrate-cache --confirm" "scenario-032-docs-migrate-help-consistency"
assert_contains "$DOC_TEXT" "backup" "scenario-032-docs-migrate-help-consistency"

SYNC_HELP="$(go run ./cmd/gitcode-mcp sync --help)"
assert_contains "$SYNC_HELP" "--live" "scenario-032-docs-sync-help-consistency"
assert_contains "$SYNC_HELP" "--issues" "scenario-032-docs-sync-help-consistency"
assert_contains "$SYNC_HELP" "--wiki" "scenario-032-docs-sync-help-consistency"
assert_contains "$SYNC_HELP" "--index" "scenario-032-docs-sync-help-consistency"
assert_contains "$SYNC_HELP" "--idempotency-key" "scenario-032-docs-sync-help-consistency"

MIGRATE_HELP="$(go run ./cmd/gitcode-mcp migrate-cache --help)"
assert_contains "$MIGRATE_HELP" "supported older versions" "scenario-032-docs-migrate-help-consistency"
assert_contains "$MIGRATE_HELP" "backup" "scenario-032-docs-migrate-help-consistency"
assert_contains "$MIGRATE_HELP" "--confirm" "scenario-032-docs-migrate-help-consistency"

SYNC_STATUS_HELP="$(go run ./cmd/gitcode-mcp sync_status --help)"
assert_contains "$SYNC_STATUS_HELP" "Report sync freshness" "scenario-032-docs-sync-status-consistency"
assert_contains "$SYNC_STATUS_HELP" "--repo" "scenario-032-docs-sync-status-consistency"
assert_contains "$SYNC_STATUS_HELP" "--kind" "scenario-032-docs-sync-status-consistency"
assert_contains "$SYNC_STATUS_HELP" "--status" "scenario-032-docs-sync-status-consistency"

OLD_CACHE="$TMPDIR/version-one.db"
python3 - <<'PY' "$OLD_CACHE"
import sqlite3, sys
path = sys.argv[1]
con = sqlite3.connect(path)
con.execute('create table schema_version (version integer not null, applied_at text not null)')
con.execute("insert into schema_version(version, applied_at) values (1, '2026-01-01T00:00:00Z')")
con.commit()
con.close()
PY

set +e
OLD_OUTPUT="$(go run ./cmd/gitcode-mcp cache-status --repo fixture-a --cache-path "$OLD_CACHE" 2>&1 >/dev/null)"
OLD_EXIT=$?
set -e
if [[ "$OLD_EXIT" -eq 0 ]]; then
  fail "scenario-032-docs-task-3-document-sync-and-migration-policy-scenario-1: version-1 cache opened successfully"
fi
assert_contains "$OLD_OUTPUT" "schema version is incompatible" "scenario-032-docs-task-3-document-sync-and-migration-policy-scenario-2"
assert_contains "$OLD_OUTPUT" "detected=1" "scenario-032-docs-task-3-document-sync-and-migration-policy-scenario-2"
assert_contains "$OLD_OUTPUT" "expected=$CURRENT_VERSION" "scenario-032-docs-task-3-document-sync-and-migration-policy-scenario-2"
assert_contains "$OLD_OUTPUT" "migrate-cache" "scenario-032-docs-task-3-document-sync-and-migration-policy-scenario-2"

for id in \
  032-docs-task-3-document-sync-and-migration-policy-scenario-1 \
  032-docs-task-3-document-sync-and-migration-policy-scenario-2 \
  032-docs-task-3-document-sync-and-migration-policy-scenario-3 \
  032-docs-task-3-document-sync-and-migration-policy-scenario-4 \
  032-docs-task-3-document-sync-and-migration-policy-scenario-5 \
  032-docs-task-3-document-sync-and-migration-policy-scenario-6; do
  if ! python3 - <<'PY' "$ROOT/tests/design_package/032-docs-task-3-document-sync-and-migration-policy/scenarios.md" "$id"
import pathlib, sys
text = pathlib.Path(sys.argv[1]).read_text()
raise SystemExit(0 if sys.argv[2] in text else 1)
PY
  then
    fail "scenarios.md missing required scenario id $id"
  fi
done

printf 'PASS: 032 docs sync and migration policy validation scenarios passed\n'
