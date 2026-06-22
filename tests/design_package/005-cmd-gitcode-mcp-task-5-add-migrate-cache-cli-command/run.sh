#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
TASK_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="${TASK_DIR}/gitcode-mcp"
TMP_DIR="$(mktemp -d)"
PASS=0
FAIL=0

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

pass() {
  echo "PASS: $1"
  PASS=$((PASS + 1))
}

fail() {
  echo "FAIL: $1"
  FAIL=$((FAIL + 1))
}

json_field() {
  python3 - "$1" "$2" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    data = json.load(f)
print(data.get(sys.argv[2], ""))
PY
}

create_current_cache() {
  local cache_path="$1"
  "${BINARY}" --cache-path "${cache_path}" repo add \
    --repo fixture-repo \
    --owner placeholder-owner \
    --name placeholder-repo \
    --api-base-url https://example.invalid \
    --scopes issues,wiki >/dev/null
}

set_schema_version() {
  local cache_path="$1"
  local version="$2"
  python3 - "${cache_path}" "${version}" <<'PY'
import sqlite3, sys
path, version = sys.argv[1], int(sys.argv[2])
conn = sqlite3.connect(path)
conn.execute("DELETE FROM schema_version")
conn.execute("INSERT INTO schema_version (version) VALUES (?)", (version,))
conn.commit()
conn.close()
PY
}

repo_count() {
  local cache_path="$1"
  python3 - "${cache_path}" <<'PY'
import sqlite3, sys
conn = sqlite3.connect(sys.argv[1])
print(conn.execute("SELECT count(*) FROM repos").fetchone()[0])
conn.close()
PY
}

echo "=== Validation: Add migrate-cache CLI command ==="
echo "Task directory: ${TASK_DIR}"
echo ""

rm -f "${BINARY}"
if (cd "${REPO_ROOT}" && go build -o "${BINARY}" ./cmd/gitcode-mcp/); then
  pass "binary builds into task validation directory"
else
  fail "binary build failed"
  echo "EXIT: ${FAIL} failures, ${PASS} passes"
  exit 1
fi

echo "--- Scenario 1: version 2 cache migrates in place and preserves data ---"
V2_CACHE="${TMP_DIR}/version2-cache.db"
create_current_cache "${V2_CACHE}"
set_schema_version "${V2_CACHE}" 2
BEFORE_REPOS="$(repo_count "${V2_CACHE}")"
if output_v2="$(${BINARY} migrate-cache --cache-path "${V2_CACHE}" --format json 2>"${TMP_DIR}/migrate-v2.stderr")"; then
  pass "migrate-cache exits 0 for version 2 cache"
else
  fail "migrate-cache failed for version 2 cache: $(cat "${TMP_DIR}/migrate-v2.stderr")"
  output_v2="{}"
fi
printf '%s' "${output_v2}" > "${TMP_DIR}/migrate-v2.json"
AFTER_REPOS="$(repo_count "${V2_CACHE}")"
if [ "${BEFORE_REPOS}" = "${AFTER_REPOS}" ] && [ "${AFTER_REPOS}" = "1" ]; then
  pass "version 2 migration preserves repository rows"
else
  fail "repository rows not preserved across migration: before=${BEFORE_REPOS} after=${AFTER_REPOS}"
fi
if [ "$(json_field "${TMP_DIR}/migrate-v2.json" status)" = "migrated" ] && [ "$(json_field "${TMP_DIR}/migrate-v2.json" from_version)" = "2" ] && [ "$(json_field "${TMP_DIR}/migrate-v2.json" to_version)" = "4" ]; then
  pass "version 2 migration reports version 2 to version 4"
else
  fail "version 2 migration JSON did not report expected version transition: ${output_v2}"
fi

echo "--- Scenario 2: version 1 cache reports incompatibility and re-initialization ---"
V1_CACHE="${TMP_DIR}/version1-cache.db"
create_current_cache "${V1_CACHE}"
set_schema_version "${V1_CACHE}" 1
if output_v1="$(${BINARY} migrate-cache --cache-path "${V1_CACHE}" --format json 2>"${TMP_DIR}/migrate-v1.stderr")"; then
  fail "migrate-cache unexpectedly exited 0 for version 1 cache"
else
  pass "migrate-cache exits non-zero for version 1 cache"
fi
if [ -z "${output_v1}" ]; then
  output_v1="$(${BINARY} migrate-cache --cache-path "${V1_CACHE}" --format json 2>/dev/null || true)"
fi
printf '%s' "${output_v1}" > "${TMP_DIR}/migrate-v1.json"
if python3 - "${TMP_DIR}/migrate-v1.json" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    data = json.load(f)
status = str(data.get("status", "")).lower()
remediation = str(data.get("remediation", "")).lower()
if status != "incompatible":
    print("status was not incompatible")
    sys.exit(1)
if "re-initialize" not in remediation and "reinitialize" not in remediation:
    print("remediation did not recommend re-initialization")
    sys.exit(1)
PY
then
  pass "version 1 cache reports incompatibility with re-initialization guidance"
else
  fail "version 1 incompatibility JSON did not include expected remediation: ${output_v1}"
fi

echo "--- Scenario 3: current cache is already up to date ---"
CURRENT_CACHE="${TMP_DIR}/current-cache.db"
create_current_cache "${CURRENT_CACHE}"
if output_current="$(${BINARY} migrate-cache --cache-path "${CURRENT_CACHE}" --format json 2>"${TMP_DIR}/migrate-current.stderr")"; then
  pass "migrate-cache exits 0 for current cache"
else
  fail "migrate-cache failed for current cache: $(cat "${TMP_DIR}/migrate-current.stderr")"
  output_current="{}"
fi
printf '%s' "${output_current}" > "${TMP_DIR}/migrate-current.json"
if [ "$(json_field "${TMP_DIR}/migrate-current.json" status)" = "up_to_date" ]; then
  pass "current cache reports up_to_date"
else
  fail "current cache did not report up_to_date: ${output_current}"
fi

echo "--- Repository checks ---"
if (cd "${REPO_ROOT}" && go test ./... >"${TMP_DIR}/go-test.txt" 2>&1); then
  pass "go test ./... passes offline"
else
  fail "go test ./... failed offline"
  cat "${TMP_DIR}/go-test.txt"
fi

if (cd "${REPO_ROOT}" && git diff --check >"${TMP_DIR}/diff-check.txt" 2>&1); then
  pass "git diff --check passes"
else
  fail "git diff --check failed"
  cat "${TMP_DIR}/diff-check.txt"
fi

echo ""
echo "EXIT: ${FAIL} failures, ${PASS} passes"
if [ "${FAIL}" -ne 0 ]; then
  exit 1
fi
