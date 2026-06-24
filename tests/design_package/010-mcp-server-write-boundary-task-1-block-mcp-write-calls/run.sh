#!/usr/bin/env bash
set -euo pipefail

# Materialize validation for task 010-mcp-server-write-boundary-task-1-block-mcp-write-calls.
# Runs all acceptance scenarios through the production MCP server test path.
# Must pass without credentials, network, SSH agent, or OS Keychain.

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

echo "=== Scenario 1: tools/list advertises only read/cache tools (no write tools) ==="
go test ./internal/mcp/ -run TestIntegration -count=1 -v 2>&1 || {
  echo "FAIL: TestIntegration (tools/list) failed"
  exit 1
}
echo "PASS: tools/list returns only read/cache tools"

echo ""
echo "=== Scenario 2: blocked write canonical 5 names return unsupported_capability ==="
go test ./internal/mcp/ -run TestMCPBlockedWriteBoundary -count=1 -v 2>&1 || {
  echo "FAIL: TestMCPBlockedWriteBoundary failed"
  exit 1
}
echo "PASS: all 5 blocked write names return unsupported_capability; read tools still work"

echo ""
echo "=== Scenario 3: go test ./... and git diff --check pass offline ==="
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
