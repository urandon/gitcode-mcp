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
  if (cd "${REPO_ROOT}" && env -u GITCODE_TOKEN -u GITCODE_MCP_TEST_KEYCHAIN_TOKEN -u GITCODE_API_URL go test "${package}" -run "${pattern}" -count=1 -v) >"${output}" 2>&1; then
    pass "${name}"
  else
    fail "${name}"
    cat "${output}"
  fi
}

printf '=== Validation: cache-runtime cache confirmations persist ===\n'
printf 'Task directory: %s\n\n' "${TASK_DIR}"

run_go_test \
  "008-cache-runtime-task-1-cache-confirmations-persist-scenario-1-cli-live-sync" \
  "./cmd/gitcode-mcp" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER$'

run_go_test \
  "008-cache-runtime-task-1-cache-confirmations-persist-scenario-1-cache-sync-evidence" \
  "./internal/cache" \
  'TestScenario008LiveSyncCacheEvidence$'

run_go_test \
  "008-cache-runtime-task-1-cache-confirmations-persist-scenario-2-cli-live-create" \
  "./cmd/gitcode-mcp" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-LIVE-WRITE-MOCK-KEYCHAIN$'

run_go_test \
  "008-cache-runtime-task-1-cache-confirmations-persist-scenario-2-service-cache-confirmation" \
  "./internal/service" \
  'TestScenario007WriteLiveCreateAuditCacheConfirmation$'

run_go_test \
  "008-cache-runtime-task-1-cache-confirmations-persist-scenario-3-service-replay" \
  "./internal/service" \
  'TestScenario007WriteLiveCreateIdempotentReplay$'

run_go_test \
  "008-cache-runtime-task-1-cache-confirmations-persist-scenario-3-cache-upsert" \
  "./internal/cache" \
  'TestScenario008CacheConfirmationIdempotentUpsert$'

run_go_test \
  "008-cache-runtime-task-1-cache-confirmations-persist-scenario-4-cache-migration-and-invariants" \
  "./internal/cache" \
  'Test(InitialMigration|Scenario008CacheConfirmationIdempotentUpsert|Scenario008CacheConfirmationRequiresRecord|Scenario008LiveSyncCacheEvidence)$'

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
