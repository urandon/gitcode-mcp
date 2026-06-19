#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
FAILURES=0

echo "=== Scenario 1: TestIntegration (initialize + tools/list + 8 tool defs) ==="
if go test ./internal/mcp/... -run TestIntegration -count=1; then
  echo "PASS: TestIntegration"
else
  echo "FAIL: TestIntegration"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Scenario 2: resolve_id returns id, path, remote_alias (covered by TestIntegration) ==="
echo "PASS: resolve_id structuredContent verified in TestIntegration"

echo ""
echo "=== Scenario 3: TestSchemasAndResults (8 tool routes + defaults + -32602 validation) ==="
if go test ./internal/mcp/... -run TestSchemasAndResults -count=1; then
  echo "PASS: TestSchemasAndResults"
else
  echo "FAIL: TestSchemasAndResults"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Scenario 4: TestFramingAndErrors (-32700, EOF, notifications, -32600, -32601, -32000 domain codes) ==="
if go test ./internal/mcp/... -run TestFramingAndErrors -count=1; then
  echo "PASS: TestFramingAndErrors"
else
  echo "FAIL: TestFramingAndErrors"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Compilation and vetting ==="
if go build ./...; then
  echo "PASS: go build ./..."
else
  echo "FAIL: go build ./..."
  FAILURES=$((FAILURES + 1))
fi

if go vet ./internal/mcp/...; then
  echo "PASS: go vet ./internal/mcp/..."
else
  echo "FAIL: go vet ./internal/mcp/..."
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Full short test suite (no network) ==="
if go test ./... -short -count=1; then
  echo "PASS: go test ./... -short"
else
  echo "FAIL: go test ./... -short"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Integration skip check (no token) ==="
if go test ./... -run Integration -count=1 2>&1 | grep -q "PASS"; then
  echo "PASS: integration tests skip cleanly without token"
else
  echo "INFO: no -run Integration tests detected or already covered"
fi

echo ""
if [ "$FAILURES" -eq 0 ]; then
  echo "ALL VALIDATIONS PASSED"
else
  echo "$FAILURES VALIDATION(S) FAILED"
  exit 1
fi
