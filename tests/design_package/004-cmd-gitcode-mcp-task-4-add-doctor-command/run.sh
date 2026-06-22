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

run_doctor_json_no_token() {
  env -u GITCODE_TOKEN -u GITCODE_MCP_CONFIG -u GITCODE_MCP_CONFIG_PATH \
    "${BINARY}" --cache-path "${TMP_DIR}/no-token-cache.db" doctor --format json
}

run_doctor_json_with_token() {
  env GITCODE_TOKEN="validation-secret-token-004" \
    "${BINARY}" --cache-path "${TMP_DIR}/token-cache.db" doctor --format json
}

echo "=== Validation: Add doctor command ==="
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

echo "--- Scenario 1: full doctor readiness report and public-safety ---"
if output_token="$(run_doctor_json_with_token 2>"${TMP_DIR}/doctor-token.stderr")"; then
  pass "doctor --format json exits 0 with token configured"
else
  fail "doctor --format json failed with token configured: $(cat "${TMP_DIR}/doctor-token.stderr")"
  output_token=""
fi
printf '%s' "${output_token}" > "${TMP_DIR}/doctor-token.json"

if python3 - "${TMP_DIR}/doctor-token.json" <<'PY'
import json, sys
path = sys.argv[1]
with open(path, 'r', encoding='utf-8') as f:
    data = json.load(f)
missing = []
for key in ["version", "config", "cache", "credential", "repo", "sync", "index", "mcp"]:
    if key not in data:
        missing.append(key)
cred = data.get("credential", {})
if "source" not in cred:
    missing.append("credential.source")
if "last_sync_at" not in data.get("sync", {}):
    missing.append("sync.last_sync_at")
if not any(k in data for k in ["live_provider", "live", "provider"]):
    missing.append("live_provider")
if not any(k in data for k in ["auth_probe", "auth", "authentication"]):
    missing.append("auth_probe")
mcp = data.get("mcp", {})
if not any(k in mcp for k in ["transport_stdio", "transport_http", "transport", "transports"]):
    missing.append("mcp.transport")
if missing:
    print("missing readiness dimensions: " + ", ".join(missing))
    sys.exit(1)
PY
then
  pass "doctor JSON includes all required readiness dimensions"
else
  fail "doctor JSON is missing one or more required readiness dimensions"
fi

if printf '%s' "${output_token}" | grep -Fq "validation-secret-token-004"; then
  fail "doctor output leaked raw token"
else
  pass "doctor output redacts raw token value"
fi
if printf '%s' "${output_token}" | grep -Eiq 'Authorization:|Bearer[[:space:]]+validation-secret-token-004|Cookie:|Set-Cookie:'; then
  fail "doctor output exposed auth header or cookie material"
else
  pass "doctor output contains no auth header or cookie material"
fi

echo "--- Scenario 2: no binding diagnostic ---"
if output_no_token="$(run_doctor_json_no_token 2>"${TMP_DIR}/doctor-no-token.stderr")"; then
  pass "doctor --format json exits 0 with no binding"
else
  fail "doctor --format json failed with no binding: $(cat "${TMP_DIR}/doctor-no-token.stderr")"
  output_no_token=""
fi
printf '%s' "${output_no_token}" > "${TMP_DIR}/doctor-no-token.json"

if python3 - "${TMP_DIR}/doctor-no-token.json" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    data = json.load(f)
repo = data.get("repo", {})
status = str(repo.get("status", "")).lower()
hint = " ".join(str(repo.get(k, "")) for k in ["bind_hint", "remediation", "suggestion"]).lower()
if "no_repo_bound" not in status and "no repo bound" not in status:
    print("repo status did not report no binding")
    sys.exit(1)
if "bind" not in hint and "repo add" not in hint:
    print("repo section did not include bind suggestion")
    sys.exit(1)
PY
then
  pass "no binding reports no_repo_bound plus bind suggestion"
else
  fail "no binding diagnostic missing no_repo_bound or bind suggestion"
fi

echo "--- Scenario 3: no token diagnostic ---"
if python3 - "${TMP_DIR}/doctor-no-token.json" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    data = json.load(f)
cred = data.get("credential", {})
status = str(cred.get("status", "")).lower()
sources = cred.get("available_sources")
if "no_token_configured" not in status and "no token configured" not in status:
    print("credential status did not report no token configured")
    sys.exit(1)
if not isinstance(sources, list) or not sources:
    print("available credential sources missing or empty")
    sys.exit(1)
PY
then
  pass "no token reports no_token_configured plus available sources"
else
  fail "no token diagnostic missing no_token_configured or available sources"
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
