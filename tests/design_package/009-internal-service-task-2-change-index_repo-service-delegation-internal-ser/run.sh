#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

echo "=== SCN-MCP-INDEX-REPO-DELEGATES-SERVICE-INDEX ==="
echo "=== SCN-MCP-INDEX-REPO-NOT-STALE-DIAGNOSTIC ==="
echo "=== SCN-MCP-INDEX-REPO-STALE-INDEX-NOT-CALLED ==="
echo ""
echo "Running index_repo delegation tests (spy + stale diagnostic) ..."

go test ./internal/mcp/... -run 'TestMCPIndexRepoDelegatesServiceIndex|TestMCPIndexRepoNotStaleDiagnostic' -count=1 -v

echo ""
echo "=== Running full MCP package tests ==="
go test ./internal/mcp/... -count=1

echo ""
echo "=== Running full service package tests ==="
go test ./internal/service/... -count=1

echo ""
echo "=== Running full repo tests ==="
go test ./...

echo ""
echo "=== Running git diff check ==="
git diff --check
