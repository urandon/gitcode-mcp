#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$REPO_ROOT"

export GIT_TERMINAL_PROMPT=0
unset GITCODE_TOKEN GITCODE_USER GITCODE_PASS

echo "=== Scenarios 1-4: unsupported MCP write capability boundary over stdio ==="
go test ./internal/mcp/ -run '^TestMCPBlockedWriteBoundary$' -count=1 -v 2>&1 || {
  echo "FAIL: unsupported capability MCP boundary scenarios failed"
  exit 1
}
echo "PASS: create_issue returns unsupported_capability, no credential/auth fallback, no provider calls, write tools absent from tools/list"

echo ""
echo "=== Package-level MCP regression gate ==="
go test ./internal/mcp/... -count=1 2>&1 || {
  echo "FAIL: go test ./internal/mcp/... failed"
  exit 1
}
echo "go test ./internal/mcp/... PASS"

echo ""
echo "=== Repository acceptance gates ==="
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
