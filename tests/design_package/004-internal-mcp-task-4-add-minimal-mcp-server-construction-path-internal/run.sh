#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
VALIDATION_DIR="$REPO_ROOT/tests/design_package/004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal"
cd "$REPO_ROOT"

SCENARIO_IDS=(
  "004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal-scenario-1"
  "004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal-scenario-2"
  "004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal-scenario-3"
  "004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal-scenario-4"
)

validate_contract() {
  python3 - "$VALIDATION_DIR/scenarios.md" "$VALIDATION_DIR/validation.json" "${SCENARIO_IDS[@]}" <<'PY'
import json
import pathlib
import sys

scenarios_path = pathlib.Path(sys.argv[1])
validation_path = pathlib.Path(sys.argv[2])
scenario_ids = sys.argv[3:]

scenarios = scenarios_path.read_text()
missing = [sid for sid in scenario_ids if sid not in scenarios]
if missing:
    raise SystemExit(f"scenarios.md missing scenario ids: {missing}")

manifest = json.loads(validation_path.read_text())
required = [
    "covered_outcome_ids",
    "covered_decommission_ids",
    "product_surfaces",
    "evidence_type",
    "freshness",
    "mocks_used",
    "production_files_modified",
]
missing_keys = [key for key in required if key not in manifest]
if missing_keys:
    raise SystemExit(f"validation.json missing keys: {missing_keys}")
if manifest["covered_outcome_ids"] != ["outcome-2"]:
    raise SystemExit(f"unexpected covered_outcome_ids: {manifest['covered_outcome_ids']!r}")
if manifest["covered_decommission_ids"] != ["decommission-2-2"]:
    raise SystemExit(f"unexpected covered_decommission_ids: {manifest['covered_decommission_ids']!r}")
if manifest["production_files_modified"] != []:
    raise SystemExit(f"validation must not modify production files: {manifest['production_files_modified']!r}")
scenario_results = manifest.get("scenario_results") or {}
missing_results = [sid for sid in scenario_ids if sid not in scenario_results]
if missing_results:
    raise SystemExit(f"validation.json missing scenario_results: {missing_results}")
PY
}

TMPDIR_VALIDATION="$(mktemp -d)"
cleanup() {
  if [ -n "${READONLY_DIR:-}" ] && [ -d "$READONLY_DIR" ]; then chmod u+w "$READONLY_DIR" 2>/dev/null || true; fi
  if [ -n "${LOCK_PID:-}" ]; then kill "$LOCK_PID" 2>/dev/null || true; wait "$LOCK_PID" 2>/dev/null || true; fi
  rm -rf "$TMPDIR_VALIDATION"
}
trap cleanup EXIT

BIN="$TMPDIR_VALIDATION/gitcode-mcp"

run_mcp() {
  local cache_path="$1"
  local input="$2"
  printf '%b' "$input" | "$BIN" --mcp --cache-path "$cache_path"
}

json_line() {
  local index="$1"
  python3 -c 'import sys
index = int(sys.argv[1])
lines = [line.strip() for line in sys.stdin if line.strip()]
if len(lines) <= index:
    raise SystemExit(f"missing JSON-RPC response line {index}; got {len(lines)} lines: {lines!r}")
print(lines[index])' "$index"
}

assert_tools_list() {
  local raw="$1"
  local want_code="$2"
  python3 - "$want_code" "$raw" <<'PY'
import json, sys
want, raw = sys.argv[1], sys.argv[2]
resp = json.loads(raw)
if resp.get("error"):
    raise SystemExit(f"JSON-RPC error: {resp['error']}")
result = resp.get("result") or {}
tools = result.get("tools") or []
names = [tool.get("name") for tool in tools]
if "doctor" not in names:
    raise SystemExit(f"doctor tool missing from tools/list: {tools}")
diag = result.get("startup_diagnostic") or (((resp.get("result") or {}).get("capabilities") or {}).get("tools") or {}).get("startup_diagnostic") or {}
if diag.get("error_class") != want:
    raise SystemExit(f"startup diagnostic {diag!r}, want {want!r}")
PY
}

assert_doctor() {
  local raw="$1"
  local want_code="$2"
  local want_text="$3"
  python3 - "$want_code" "$want_text" "$raw" <<'PY'
import json, sys
want, text, raw = sys.argv[1], sys.argv[2], sys.argv[3]
resp = json.loads(raw)
if resp.get("error"):
    raise SystemExit(f"JSON-RPC error: {resp['error']}")
structured = ((resp.get("result") or {}).get("structuredContent") or {})
diags = structured.get("diagnostics") or []
if structured.get("status") != "degraded" or len(diags) != 1:
    raise SystemExit(f"unexpected doctor body: {structured!r}")
diag = diags[0]
if diag.get("code") != want or not diag.get("message") or text not in (diag.get("remediation") or ""):
    raise SystemExit(f"doctor diagnostic {diag!r}, want code {want!r} remediation containing {text!r}")
stack_markers = ("panic", "goroutine", "runtime/debug", "debug.stack")
text_blob = " ".join(str(diag.get(key, "")).lower() for key in ("message", "remediation"))
if any(marker in text_blob for marker in stack_markers):
    raise SystemExit(f"doctor leaked raw stack-like text: {diag!r}")
PY
}

INPUT_LIST='{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n'
INPUT_LIST_DOCTOR='{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"doctor","arguments":{}}}\n'

echo "=== validation contract ==="
validate_contract
echo "PASS"

go build -o "$BIN" ./cmd/gitcode-mcp

echo "=== 004 scenario 1: read-only cache path yields cache_path_unwritable ==="
READONLY_DIR="$TMPDIR_VALIDATION/read-only-cache-dir"
mkdir "$READONLY_DIR"
chmod 500 "$READONLY_DIR"
set +e
OUT="$(run_mcp "$READONLY_DIR/missing/cache.db" "$INPUT_LIST" 2>&1)"
STATUS=$?
set -e
if [ "$STATUS" -ne 0 ]; then echo "$OUT"; echo "FAIL: MCP exited non-zero for read-only cache path"; exit 1; fi
LINE1="$(printf '%s\n' "$OUT" | json_line 0)"
assert_tools_list "$LINE1" "cache_path_unwritable"
echo "PASS"

echo "=== 004 scenario 2: future schema yields schema_incompatible ==="
SCHEMA_CACHE="$TMPDIR_VALIDATION/future-schema.db"
python3 - "$SCHEMA_CACHE" <<'PY'
import sqlite3, sys
conn = sqlite3.connect(sys.argv[1])
conn.execute("CREATE TABLE schema_version (version INTEGER NOT NULL)")
conn.execute("INSERT INTO schema_version (version) VALUES (99)")
conn.commit()
conn.close()
PY
OUT="$(run_mcp "$SCHEMA_CACHE" "$INPUT_LIST_DOCTOR")"
LINE1="$(printf '%s\n' "$OUT" | json_line 0)"
LINE2="$(printf '%s\n' "$OUT" | json_line 1)"
assert_tools_list "$LINE1" "schema_incompatible"
assert_doctor "$LINE2" "schema_incompatible" "upgrade"
echo "PASS"

echo "=== 004 scenario 3: writer lock yields cache_lock_contention ==="
LOCK_CACHE="$TMPDIR_VALIDATION/locked-cache.db"
LOCK_FILE="$LOCK_CACHE.writer.lock"
python3 - "$LOCK_FILE" <<'PY' &
import fcntl, pathlib, sys, time
p = pathlib.Path(sys.argv[1])
p.parent.mkdir(parents=True, exist_ok=True)
with p.open("w") as f:
    f.write('{"operation":"validation-writer","cache_path":"validation"}')
    f.flush()
    fcntl.flock(f.fileno(), fcntl.LOCK_EX)
    time.sleep(60)
PY
LOCK_PID=$!
sleep 1
OUT="$(run_mcp "$LOCK_CACHE" "$INPUT_LIST")"
LINE1="$(printf '%s\n' "$OUT" | json_line 0)"
assert_tools_list "$LINE1" "cache_lock_contention"
echo "PASS"

echo "=== 004 scenario 4: injected cache init failure yields startup-failure doctor ==="
BROKEN_PARENT="$TMPDIR_VALIDATION/not-a-directory"
printf 'not a directory' > "$BROKEN_PARENT"
BROKEN_CACHE="$BROKEN_PARENT/cache.db"
OUT="$(run_mcp "$BROKEN_CACHE" "$INPUT_LIST_DOCTOR")"
LINE1="$(printf '%s\n' "$OUT" | json_line 0)"
LINE2="$(printf '%s\n' "$OUT" | json_line 1)"
assert_tools_list "$LINE1" "startup-failure"
assert_doctor "$LINE2" "startup-failure" "doctor"
echo "PASS"

echo "=== Required gates ==="
go test ./internal/mcp/... ./cmd/gitcode-mcp/... -count=1
git diff --check
echo "ALL SCENARIOS PASSED"
