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

printf '=== Validation: sync-service live reconciliation ===\n'
printf 'Task directory: %s\n\n' "${TASK_DIR}"

run_go_test \
  "006-sync-service-task-1-syncgraph-live-reconciliation-scenario-1-cli-live-sync-route" \
  "./cmd/gitcode-mcp" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER$'

run_go_test \
  "006-sync-service-task-1-syncgraph-live-reconciliation-scenario-1-live-graph-cache-comments" \
  "./internal/service" \
  'TestScenario006LiveGraphValidStagesIssueWikiComments$'

run_go_test \
  "006-sync-service-task-1-syncgraph-live-reconciliation-scenario-2-cli-invalid-token-live-auth" \
  "./cmd/gitcode-mcp" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-LIVE-SYNC-INVALID-TOKEN-AUTH-FAILURE$'

run_go_test \
  "006-sync-service-task-1-syncgraph-live-reconciliation-scenario-2-normalize-live-auth" \
  "./internal/service" \
  'TestScenario006LiveAuthFailureNormalized$'

run_go_test \
  "006-sync-service-task-1-syncgraph-live-reconciliation-scenario-3-invalid-live-graph-rejected" \
  "./internal/service" \
  'TestScenario006LiveGraphInvalidRejectedBeforeCommit$'

run_go_test \
  "006-sync-service-task-1-syncgraph-live-reconciliation-scenario-4-default-sync-no-http" \
  "./cmd/gitcode-mcp" \
  'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-OFFLINE-SYNC-NO-HTTP$'

run_go_test \
  "006-sync-service-task-1-syncgraph-live-reconciliation-scenario-4-fixture-default-preserved" \
  "./internal/service" \
  'TestSyncResourcesCachesFixtureRecords$'

run_go_test \
  "006-sync-service-task-1-syncgraph-live-reconciliation-scenario-5-bulk-live-partial-atomic" \
  "./internal/service" \
  'TestScenario006BulkLiveSyncPartialAtomicity$'

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
