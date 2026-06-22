#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

cd "$ROOT"

# Stage 1: Run the Go test suite that exercises all HTTP/SSE and stdio transport scenarios
echo "==> Running Go transport tests"
go test ./internal/mcp -count=1 -run 'TestHTTPSSETransportSessionFlow|TestHTTPSSETransportSessionErrorsAndMultiClient|TestHTTPSSETransportDeduplicatesSessionIDs|TestHTTPSSECancelledSessionDoesNotBlockOtherClient|TestHTTPSSEReadinessLockContention|TestIntegration|TestMCPReadToolParityOverStdio|TestMCPReadToolParityOverHTTPSSE' -v

# Stage 2: Verify the full test suite passes
echo "==> Running full test suite"
go test ./...

# Stage 3: Verify no whitespace issues in tracked files
echo "==> Checking git diff whitespace"
git diff --check

# Stage 4: Build the binary and exercise the real entrypoint paths
BIN="$TMPDIR/gitcode-mcp"
CACHE="$TMPDIR/cache.db"
STDOUT_LOG="$TMPDIR/mcp_stdout.jsonl"
STDERR_LOG="$TMPDIR/mcp_stderr.log"
REPORT="$TMPDIR/report.json"

go build -o "$BIN" ./cmd/gitcode-mcp

echo "==> Stage 4a: stdio MCP single-client (scenario-1 stdio)"
# Populate the cache via the CLI so the MCP server has data to read
# repo_id is derived from --repo flag; --owner and --api-base-url are required for repo add
"$BIN" --cache-path "$CACHE" repo add --owner owner-a --repo repo-a --name fixture-a --api-base-url https://example.invalid/api --scopes issues,wiki 2>> "$STDERR_LOG"
"$BIN" --cache-path "$CACHE" sync --repo repo-a 2>> "$STDERR_LOG" || true

cat > "$TMPDIR/stdio_requests.jsonl" <<'JSONL'
{"jsonrpc":"2.0","id":1,"method":"initialize"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_sources","arguments":{"repo_id":"repo-a"}}}
JSONL

"$BIN" --mcp --cache-path "$CACHE" < "$TMPDIR/stdio_requests.jsonl" > "$STDOUT_LOG" 2> "$STDERR_LOG"

python3 - "$STDOUT_LOG" "$STDERR_LOG" "$REPORT" <<'PY'
import json
import sys
from pathlib import Path

stdout_path = Path(sys.argv[1])
stderr_path = Path(sys.argv[2])
report_path = Path(sys.argv[3])

errors = []
scenario_results = {}

lines = [line for line in stdout_path.read_text().splitlines() if line.strip()]
if len(lines) < 3:
    errors.append("stdio MCP: expected at least 3 response lines for initialize, tools/list, tools/call")
    scenario_results["027-internal-mcp-transport-task-1-add-http-sse-transport-handler-with-health-readine-scenario-1"] = {"status": "FAIL", "details": "insufficient response lines"}
else:
    responses = []
    for line in lines:
        try:
            responses.append(json.loads(line))
        except json.JSONDecodeError as exc:
            errors.append(f"stdio MCP: invalid JSON-RPC response: {line!r}")
    if not errors:
        by_id = {r.get("id"): r for r in responses}

        init_resp = by_id.get(1)
        if not init_resp:
            errors.append("stdio MCP: missing initialize response id=1")
        elif init_resp.get("error"):
            errors.append(f"stdio MCP: initialize returned error: {init_resp['error']}")
        else:
            result = init_resp.get("result", {})
            info = result.get("serverInfo", {})
            if info.get("name") != "gitcode-mcp":
                errors.append(f"stdio MCP: initialize serverInfo.name={info.get('name')!r}, want gitcode-mcp")

        tools_resp = by_id.get(2)
        if not tools_resp:
            errors.append("stdio MCP: missing tools/list response id=2")
        elif tools_resp.get("error"):
            errors.append(f"stdio MCP: tools/list returned error: {tools_resp['error']}")

        list_resp = by_id.get(3)
        if not list_resp:
            errors.append("stdio MCP: missing list_sources response id=3")
        elif list_resp.get("error"):
            errors.append(f"stdio MCP: list_sources returned error: {list_resp['error']}")

        scenario_results["027-internal-mcp-transport-task-1-add-http-sse-transport-handler-with-health-readine-scenario-1"] = {"status": "PASS" if not errors else "FAIL", "details": "stdio single-client transport"}

scenario_results["027-internal-mcp-transport-task-1-add-http-sse-transport-handler-with-health-readine-scenario-2"] = {"status": "delegated", "details": "verified by Go tests: TestHTTPSSETransportSessionErrorsAndMultiClient, TestHTTPSSETransportDeduplicatesSessionIDs, TestHTTPSSECancelledSessionDoesNotBlockOtherClient"}
scenario_results["027-internal-mcp-transport-task-1-add-http-sse-transport-handler-with-health-readine-scenario-3"] = {"status": "delegated", "details": "verified by Go tests: TestHTTPSSETransportSessionFlow (X-Request-ID header propagation), TestHTTPSSETransportSessionErrorsAndMultiClient (transport error correlation IDs in logs)"}

report = {
    "scenario_results": scenario_results,
    "passed": len(errors) == 0,
    "errors": errors,
}
report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
if errors:
    for error in errors:
        print(f"VALIDATION FAILURE: {error}", file=sys.stderr)
    raise SystemExit(1)
print(json.dumps(report, indent=2, sort_keys=True))
PY

# Script exit determined by Python above
