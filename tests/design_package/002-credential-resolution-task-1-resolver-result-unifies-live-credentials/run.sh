#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
TASK_DIR="$(cd "$(dirname "$0")" && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

pass_count=0
fail_count=0

pass() {
  printf 'PASS: %s\n' "$1"
  pass_count=$((pass_count + 1))
}

fail() {
  printf 'FAIL: %s\n' "$1"
  fail_count=$((fail_count + 1))
}

run_go_test() {
  local name="$1"
  local pattern="$2"
  local output="${TMP_DIR}/${name}.txt"
  if (cd "${REPO_ROOT}" && env -u GITCODE_TOKEN -u GITCODE_MCP_TEST_KEYCHAIN_TOKEN -u GITCODE_API_URL go test ./cmd/gitcode-mcp -run "${pattern}" -count=1 -v) >"${output}" 2>&1; then
    pass "${name}"
  else
    fail "${name}"
    cat "${output}"
  fi
}

printf '=== Validation: credential-resolution live resolver result ===\n'
printf 'Task directory: %s\n\n' "${TASK_DIR}"

run_go_test \
  "002-credential-resolution-task-1-resolver-result-unifies-live-credentials-scenario-1" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-MISSING-CREDENTIAL$'

run_go_test \
  "002-credential-resolution-task-1-resolver-result-unifies-live-credentials-scenario-2" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-LIVE-WRITE-MOCK-KEYCHAIN$'

run_go_test \
  "002-credential-resolution-task-1-resolver-result-unifies-live-credentials-scenario-3" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-DOCTOR-LIVE-MOCK-KEYCHAIN$'

full_output="${TMP_DIR}/go-test-all.txt"
if (cd "${REPO_ROOT}" && env -u GITCODE_TOKEN -u GITCODE_MCP_TEST_KEYCHAIN_TOKEN -u GITCODE_API_URL go test ./...) >"${full_output}" 2>&1; then
  pass "go test ./... offline acceptance gate"
else
  fail "go test ./... offline acceptance gate"
  cat "${full_output}"
fi

printf '\nEXIT: %d failures, %d passes\n' "${fail_count}" "${pass_count}"
if [ "${fail_count}" -ne 0 ]; then
  exit 1
fi
