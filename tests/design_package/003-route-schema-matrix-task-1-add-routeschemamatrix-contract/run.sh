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
unset SSH_AUTH_SOCK
unset SSH_AGENT_PID

export GONOSUMDB='*'
export GOPRIVATE=''

# Scenario 1: Default matrix shape, construction, issues-reaches-HTTP, classifier
echo "=== Scenario 1: Matrix default shape + live provider construction + issues HTTP ==="
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixDefaultShape$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixLiveProviderConstruction$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixIssuesReachesHTTP$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixSpecLookup$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixRequireDeclared$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixValidateCoverageSuccess$" -count=1 -v
go test ./internal/diagnostics -run "^TestClassifierUnsupportedCapability$" -count=1 -v

# Scenario 2: PR/comment preflight blocks HTTP with unsupported_capability diagnostics
echo "=== Scenario 2: PR/comment preflight + ErrUnsupportedCapability diagnostics ==="
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixCommentsPreflightBlocksHTTP$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixPreflight$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005ErrUnsupportedCapabilityDiagnosticCode$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005IsUnsupportedCapability$" -count=1 -v

# Scenario 3: Matrix validation rejection — all mutation cases fail deterministically
echo "=== Scenario 3: Matrix validation rejection (missing areas, bad enums, contradictory specs) ==="
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixValidateCoverageMissingArea$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixValidateCoverageBadEnums$" -count=1 -v
go test ./internal/gitcode -run "^TestScenario005RouteSchemaMatrixValidateCoverageContradictorySpec$" -count=1 -v

# Full offline suite pass + whitespace check
echo "=== Full offline suite + whitespace check ==="
go test ./... -count=1
git diff --check
