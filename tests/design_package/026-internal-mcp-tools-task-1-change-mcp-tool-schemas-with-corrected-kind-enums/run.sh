#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

BIN="$TMPDIR/gitcode-mcp"
CACHE="$TMPDIR/cache.db"
STDOUT_JSONL="$TMPDIR/mcp_stdout.jsonl"
STDERR_LOG="$TMPDIR/mcp_stderr.log"
REQUESTS="$TMPDIR/requests.jsonl"
REPORT="$TMPDIR/report.json"

cd "$ROOT"
go build -o "$BIN" ./cmd/gitcode-mcp

cat > "$REQUESTS" <<'JSONL'
{"jsonrpc":"2.0","id":1,"method":"initialize"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_sources","arguments":{"repo_id":"fixture-a","kind":"task"}}}
JSONL

"$BIN" --mcp --cache-path "$CACHE" < "$REQUESTS" > "$STDOUT_JSONL" 2> "$STDERR_LOG"

python3 - "$STDOUT_JSONL" "$STDERR_LOG" "$REPORT" <<'PY'
import json
import sys
from pathlib import Path

stdout_path = Path(sys.argv[1])
stderr_path = Path(sys.argv[2])
report_path = Path(sys.argv[3])
legacy = {"source", "task", "page", "decision", "handoff"}
required_tools = ["list_sources", "search_sources", "search_chunks"]
expected = ["issue", "wiki"]

lines = [line for line in stdout_path.read_text().splitlines() if line.strip()]
responses = []
for line in lines:
    try:
        responses.append(json.loads(line))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"invalid JSON-RPC response line: {exc}: {line!r}")

by_id = {resp.get("id"): resp for resp in responses}
errors = []
scenario_results = {}

tools_resp = by_id.get(2)
if not tools_resp:
    errors.append("missing tools/list response id=2")
elif tools_resp.get("error"):
    errors.append(f"tools/list returned error: {tools_resp['error']}")
else:
    tools = {tool.get("name"): tool for tool in tools_resp.get("result", {}).get("tools", [])}
    enum_report = {}
    for name in required_tools:
        tool = tools.get(name)
        if not tool:
            errors.append(f"missing required MCP tool {name}")
            continue
        props = tool.get("inputSchema", {}).get("properties", {})
        kind = props.get("kind")
        if not kind:
            errors.append(f"{name} schema missing kind property")
            continue
        enum = kind.get("enum")
        enum_report[name] = enum
        if enum != expected:
            errors.append(f"{name} kind enum = {enum!r}, want {expected!r}")
        present_legacy = sorted(legacy.intersection(enum or []))
        if present_legacy:
            errors.append(f"{name} kind enum contains legacy values: {present_legacy}")
    scenario_results["026-internal-mcp-tools-task-1-change-mcp-tool-schemas-with-corrected-kind-enums-scenario-1"] = enum_report
    scenario_results["026-internal-mcp-tools-task-1-change-mcp-tool-schemas-with-corrected-kind-enums-scenario-2"] = enum_report

invalid_resp = by_id.get(3)
if not invalid_resp:
    errors.append("missing invalid legacy kind tools/call response id=3")
else:
    err = invalid_resp.get("error")
    if not err:
        errors.append("legacy kind task was accepted by list_sources")
    elif err.get("code") != -32602:
        errors.append(f"legacy kind task error code = {err.get('code')!r}, want -32602")
    else:
        data = err.get("data") or {}
        message = data.get("message") or ""
        if "issue" not in message or "wiki" not in message:
            errors.append(f"invalid-kind message does not mention issue and wiki: {message!r}")
        legacy_in_message = sorted(value for value in legacy if value in message)
        if legacy_in_message:
            errors.append(f"invalid-kind message mentions legacy values: {legacy_in_message}")

stderr = stderr_path.read_text()
if stderr.strip():
    errors.append(f"MCP server wrote stderr: {stderr.strip()!r}")

report = {
    "scenario_results": scenario_results,
    "invalid_kind_response": invalid_resp,
    "passed": not errors,
    "errors": errors,
}
report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
if errors:
    for error in errors:
        print(f"VALIDATION FAILURE: {error}", file=sys.stderr)
    raise SystemExit(1)
print(json.dumps(report, indent=2, sort_keys=True))
PY

go test ./internal/mcp
git diff --check -- tests/design_package/026-internal-mcp-tools-task-1-change-mcp-tool-schemas-with-corrected-kind-enums
