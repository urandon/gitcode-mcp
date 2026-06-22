#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

unset GITCODE_TOKEN
unset GITCODE_API_BASE_URL
unset GITCODE_E2E_OWNER
unset GITCODE_E2E_REPO
unset GITCODE_E2E_TOKEN
unset GITCODE_MCP_CONFIG
unset GITCODE_MCP_TEST_KEYCHAIN_TOKEN

export GONOSUMDB='*'
export GOPRIVATE=''

run_go_test() {
  go test "$@"
}

mockapi_scenarios='TestCLIStartupPlanSelectsLiveProvider/(SCN-MOCKAPI-LIVE-SYNC-VALID|SCN-MOCKAPI-LIVE-SYNC-MISSING-CREDENTIAL|SCN-MOCKAPI-LIVE-SYNC-INVALID-TOKEN-401|SCN-MOCKAPI-LIVE-CREATE-ISSUE|SCN-MOCKAPI-OFFLINE-SYNC-NO-HTTP|SCN-MOCKAPI-API-BASE-AUTHORITY)$'

run_go_test ./cmd/gitcode-mcp -run "$mockapi_scenarios" -count=1
run_go_test ./... -count=1
git diff --check
