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
unset GITCODE_OWNER
unset GITCODE_REPO
unset GITCODE_REPOSITORY

export GONOSUMDB='*'
export GOPRIVATE=''

focused_gitcode='TestScenario00(1WikiContentsRootTraversal|2WikiMalformedEntrySchemaDecode|3WikiDuplicatePathDedup|4WikiNestingLimit|5WikiRawReadBody|6WikiCreateBase64NoSha|7WikiUpdateShaAutoresolve|8WikiUpdateExplicitSha|9WikiDeleteStaleSha409)$|TestScenario010BrowserRouteExclusion$'
focused_cli='TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-SYNC-VALID$'

go test ./internal/gitcode -run "$focused_gitcode" -count=1
go test ./cmd/gitcode-mcp -run "$focused_cli" -count=1
go test ./... -count=1
git diff --check
