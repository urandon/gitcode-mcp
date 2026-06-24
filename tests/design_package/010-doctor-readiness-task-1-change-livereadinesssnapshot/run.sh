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

run_go_test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/(SCN-CLI-DOCTOR-LIVE-JSON-STARTUP-SNAPSHOT|SCN-CRED-DOCTOR-LIVE-MOCK-KEYCHAIN|SCN-CLI-DOCTOR-LIVE-JSON-SELECTED-VS-NON-SELECTED|SCN-CLI-DOCTOR-LIVE-JSON-MISSING-CREDENTIAL-NO-HTTP)$' -count=1
run_go_test ./internal/doctor -run 'TestLiveReadiness(SelectsEffectiveBinding|RepoSelectorSwitchesBinding|MissingCredentialPreservesEffectiveValues|InvalidAPIBaseURLPrecedesCredential)|TestBuildRedactsOutput' -count=1
run_go_test ./... -count=1
git diff --check
