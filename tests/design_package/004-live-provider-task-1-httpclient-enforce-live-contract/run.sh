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
  local package="$2"
  local pattern="$3"
  local output="${TMP_DIR}/${name}.txt"
  if (cd "${REPO_ROOT}" && env -u GITCODE_TOKEN -u GITCODE_MCP_TEST_KEYCHAIN_TOKEN -u GITCODE_API_URL -u GITCODE_LIVE_TEST -u GITCODE_LIVE_TOKEN -u GITCODE_TEST_TOKEN go test "${package}" -run "${pattern}" -count=1 -v) >"${output}" 2>&1; then
    pass "${name}"
  else
    fail "${name}"
    cat "${output}"
  fi
}

printf '=== Validation: live-provider HTTPClient live contract ===\n'
printf 'Task directory: %s\n\n' "${TASK_DIR}"

run_go_test \
  "004-live-provider-task-1-httpclient-enforce-live-contract-scenario-1-cli-live-sync" \
  "./cmd/gitcode-mcp" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER$'

run_go_test \
  "004-live-provider-task-1-httpclient-enforce-live-contract-scenario-1-provider-read-contract" \
  "./internal/gitcode" \
  'TestScenario004ReadRouteContract$'

run_go_test \
  "004-live-provider-task-1-httpclient-enforce-live-contract-scenario-2-auth-after-request" \
  "./internal/gitcode" \
  'TestScenario004AuthAfterRequest$'

run_go_test \
  "004-live-provider-task-1-httpclient-enforce-live-contract-scenario-3-cli-mock-keychain-write" \
  "./cmd/gitcode-mcp" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-LIVE-WRITE-MOCK-KEYCHAIN$'

run_go_test \
  "004-live-provider-task-1-httpclient-enforce-live-contract-scenario-3-provider-create-contract" \
  "./internal/gitcode" \
  'TestScenario004CreateIssueContract$'

run_go_test \
  "004-live-provider-task-1-httpclient-enforce-live-contract-scenario-4-cli-selected-base-url" \
  "./cmd/gitcode-mcp" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-API-BASE-AUTHORITY$'

run_go_test \
  "004-live-provider-task-1-httpclient-enforce-live-contract-scenario-4-provider-selected-base-url" \
  "./internal/gitcode" \
  'TestScenario004SelectedBaseURLOnly$'

run_go_test \
  "004-live-provider-admission-fail-closed" \
  "./internal/gitcode" \
  'TestScenario004LiveProviderAdmission$'

full_output="${TMP_DIR}/go-test-all.txt"
if (cd "${REPO_ROOT}" && env -u GITCODE_TOKEN -u GITCODE_MCP_TEST_KEYCHAIN_TOKEN -u GITCODE_API_URL -u GITCODE_LIVE_TEST -u GITCODE_LIVE_TOKEN -u GITCODE_TEST_TOKEN go test ./...) >"${full_output}" 2>&1; then
  pass "go test ./... offline acceptance gate"
else
  fail "go test ./... offline acceptance gate"
  cat "${full_output}"
fi

printf '\nEXIT: %d failures, %d passes\n' "${fail_count}" "${pass_count}"
if [ "${fail_count}" -ne 0 ]; then
  exit 1
fi
