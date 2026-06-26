#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$REPO_ROOT"

export GIT_TERMINAL_PROMPT=0
unset GITCODE_TOKEN GITCODE_USER GITCODE_PASS

echo "=== Scenario 1-5: lifecycle MCP tools over production stdio path ==="
go test ./internal/mcp/ -run '^TestMCPLifecycleTools$' -count=1 -v 2>&1 || {
  echo "FAIL: lifecycle MCP tool scenarios failed"
  exit 1
}
echo "PASS: lifecycle tools listed, repo_status nothing_bound, sync_live fresh_count, index_repo Service.Index, list_sources synced records"

echo ""
echo "=== Supporting read parity: search_sources/list_sources remain usable ==="
go test ./internal/mcp/ -run '^TestMCPReadToolParityOverStdio$' -count=1 -v 2>&1 || {
  echo "FAIL: MCP read parity over stdio failed"
  exit 1
}
echo "PASS: search_sources/list_sources read path remains usable"

echo ""
echo "=== Final offline acceptance gates ==="
go test ./... -count=1 2>&1 || {
  echo "FAIL: go test ./... failed"
  exit 1
}
echo "go test ./... PASS"

git diff --check 2>&1 || {
  echo "FAIL: git diff --check reported whitespace issues"
  exit 1
}
echo "git diff --check PASS"

echo ""
echo "=== ALL SCENARIOS PASSED ==="
