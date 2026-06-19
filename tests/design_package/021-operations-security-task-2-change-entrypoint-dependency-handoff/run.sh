#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

export GIT_TERMINAL_PROMPT=0
export GOPROXY=off
export GONOSUMDB='*'
unset GITCODE_TOKEN
unset GITCODE_CONFIG

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT
BIN="$TMPDIR/gitcode-mcp"

go build ./...
go build -o "$BIN" ./cmd/gitcode-mcp

go test ./cmd/gitcode-mcp ./internal/... -run 'TestEntrypoint|TestConfigLoading|TestCLIFlagOverride|TestIntegration' -count=1

"$BIN" --help >"$TMPDIR/help.stdout" 2>"$TMPDIR/help.stderr"
if ! grep -q -- '--mcp' "$TMPDIR/help.stdout"; then
  printf 'default help did not document --mcp\n' >&2
  exit 1
fi
if [ -s "$TMPDIR/help.stderr" ]; then
  printf 'default help wrote stderr: %s\n' "$(cat "$TMPDIR/help.stderr")" >&2
  exit 1
fi

"$BIN" --mcp --help >"$TMPDIR/mcp-help.stdout" 2>"$TMPDIR/mcp-help.stderr"
if [ -s "$TMPDIR/mcp-help.stdout" ]; then
  printf 'MCP help contaminated stdout: %s\n' "$(cat "$TMPDIR/mcp-help.stdout")" >&2
  exit 1
fi
if ! grep -q 'stdio MCP' "$TMPDIR/mcp-help.stderr"; then
  printf 'MCP help missing startup text\n' >&2
  exit 1
fi

set +e
"$BIN" --cache-path "$TMPDIR/cli-cache.db" search test >"$TMPDIR/search.stdout" 2>"$TMPDIR/search.stderr"
SEARCH_STATUS=$?
set -e
if grep -q 'unknown command' "$TMPDIR/search.stderr"; then
  printf 'search did not reach default CLI route\n' >&2
  exit 1
fi
if ! grep -q 'no cached search results' "$TMPDIR/search.stderr"; then
  printf 'unexpected search route evidence status=%s stdout=%s stderr=%s\n' "$SEARCH_STATUS" "$(cat "$TMPDIR/search.stdout")" "$(cat "$TMPDIR/search.stderr")" >&2
  exit 1
fi

printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n' | "$BIN" --mcp --cache-path "$TMPDIR/mcp-cache.db" --timeout 10s >"$TMPDIR/mcp.stdout" 2>"$TMPDIR/mcp.stderr"
if [ -s "$TMPDIR/mcp.stderr" ]; then
  printf 'MCP initialize wrote stderr: %s\n' "$(cat "$TMPDIR/mcp.stderr")" >&2
  exit 1
fi
python3 - "$TMPDIR/mcp.stdout" <<'PY'
import json, sys
raw = open(sys.argv[1], encoding='utf-8').read().strip()
try:
    data = json.loads(raw)
except Exception as exc:
    raise SystemExit(f'MCP stdout is not JSON: {raw!r}: {exc}')
if data.get('jsonrpc') != '2.0':
    raise SystemExit(f'bad jsonrpc: {data!r}')
if data.get('result', {}).get('serverInfo', {}).get('name') != 'gitcode-mcp':
    raise SystemExit(f'missing server info: {data!r}')
PY

set +e
GITCODE_CONFIG="$TMPDIR/missing-config.json" GITCODE_TOKEN='validation-sentinel-token' "$BIN" --mcp >"$TMPDIR/redact.stdout" 2>"$TMPDIR/redact.stderr"
REDACT_STATUS=$?
set -e
if [ "$REDACT_STATUS" -eq 0 ]; then
  printf 'MCP missing explicit config unexpectedly succeeded\n' >&2
  exit 1
fi
if [ -s "$TMPDIR/redact.stdout" ]; then
  printf 'MCP config error contaminated stdout: %s\n' "$(cat "$TMPDIR/redact.stdout")" >&2
  exit 1
fi
if grep -q 'validation-sentinel-token' "$TMPDIR/redact.stderr"; then
  printf 'MCP config error leaked token\n' >&2
  exit 1
fi

GITCODE_CONFIG="$TMPDIR/startup.json" GITCODE_TOKEN='validation-sentinel-token' "$BIN" --mcp --help >"$TMPDIR/token-help.stdout" 2>"$TMPDIR/token-help.stderr"
if grep -q 'validation-sentinel-token' "$TMPDIR/token-help.stdout" "$TMPDIR/token-help.stderr"; then
  printf 'help leaked token\n' >&2
  exit 1
fi

go test ./... -count=1
git diff --check
