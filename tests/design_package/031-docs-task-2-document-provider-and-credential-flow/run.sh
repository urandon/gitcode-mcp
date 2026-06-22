#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/031-docs-task-2-document-provider-and-credential-flow"
TMPDIR="$(mktemp -d "$SCENARIO_DIR/tmp.XXXXXX")"
cleanup() {
  rm -rf "$TMPDIR"
}
trap cleanup EXIT

cd "$ROOT"

fail() {
  printf 'VALIDATION FAILURE: %s\n' "$*" >&2
  exit 1
}

require_file_contains() {
  local file="$1"
  local needle="$2"
  local label="$3"
  if ! python3 - "$file" "$needle" <<'PY'
import sys
from pathlib import Path
text = Path(sys.argv[1]).read_text(errors="replace")
needle = sys.argv[2]
raise SystemExit(0 if needle in text else 1)
PY
  then
    printf '\n--- %s (%s) ---\n' "$label" "$file" >&2
    sed -n '1,220p' "$file" >&2 || true
    fail "$label missing expected text: $needle"
  fi
}

require_ordered() {
  local file="$1"
  shift
  if ! python3 - "$file" "$@" <<'PY'
import sys
from pathlib import Path
text = Path(sys.argv[1]).read_text(errors="replace")
pos = -1
for needle in sys.argv[2:]:
    nxt = text.find(needle, pos + 1)
    if nxt < 0:
        print(f"missing or out of order: {needle}", file=sys.stderr)
        raise SystemExit(1)
    pos = nxt
PY
  then
    printf '\n--- ordered check failed (%s) ---\n' "$file" >&2
    sed -n '1,220p' "$file" >&2 || true
    fail "$file did not contain expected ordered text: $*"
  fi
}

require_file_lacks() {
  local file="$1"
  local needle="$2"
  local label="$3"
  if python3 - "$file" "$needle" <<'PY'
import sys
from pathlib import Path
text = Path(sys.argv[1]).read_text(errors="replace")
needle = sys.argv[2]
raise SystemExit(0 if needle in text else 1)
PY
  then
    printf '\n--- %s leaked unexpected text (%s) ---\n' "$label" "$file" >&2
    sed -n '1,220p' "$file" >&2 || true
    fail "$label contained unexpected text: $needle"
  fi
}

run_expect_success() {
  local outfile="$1"
  shift
  set +e
  "$@" > "$outfile" 2>&1
  local status=$?
  set -e
  if [[ $status -ne 0 ]]; then
    printf '\n--- command failed (%s): %s ---\n' "$status" "$*" >&2
    sed -n '1,220p' "$outfile" >&2 || true
    fail "expected command to exit 0: $*"
  fi
}

printf '==> Building real gitcode-mcp CLI\n'
BIN="$TMPDIR/gitcode-mcp"
go build -o "$BIN" ./cmd/gitcode-mcp

ARCH="docs/architecture.md"
[[ -f "$ARCH" ]] || fail "docs/architecture.md is missing"

printf '==> Scenario 1: architecture documents provider modes\n'
require_file_contains "$ARCH" "## Provider Selection" "architecture provider selection"
require_file_contains "$ARCH" "Provider mode is resolved once at command start" "architecture provider selection"
require_file_contains "$ARCH" "\`fixture\`" "architecture provider selection"
require_file_contains "$ARCH" "\`live\`" "architecture provider selection"
require_file_contains "$ARCH" "\`unavailable\`" "architecture provider selection"
require_file_contains "$ARCH" "default mode when \`--live\` is absent" "architecture provider selection"

printf '==> Scenarios 2/3: sync help exposes --live matching provider predicate\n'
run_expect_success "$TMPDIR/sync-help.out" "$BIN" sync --help
require_file_contains "$TMPDIR/sync-help.out" "Usage: gitcode-mcp sync" "sync help"
require_file_contains "$TMPDIR/sync-help.out" "--live" "sync help"
require_file_contains "$TMPDIR/sync-help.out" "live" "sync help"
require_file_contains "$ARCH" "\`--live\` plus credential" "architecture provider predicate"
require_file_contains "$ARCH" "\`--live\` plus no credential" "architecture provider predicate"
require_file_contains "$ARCH" "no \`--live\`" "architecture provider predicate"
require_ordered "$ARCH" "\`--live\` plus credential" "\`--live\` plus no credential" "no \`--live\`"

printf '==> Scenarios 4/5: auth help matches credential source chain and omits secrets\n'
require_file_contains "$ARCH" "## Credential Pipeline" "architecture credential pipeline"
require_ordered "$ARCH" "\`GITCODE_TOKEN\` environment variable" "Keychain source" "None"
run_expect_success "$TMPDIR/auth-status-help.out" "$BIN" auth status --help
require_file_contains "$TMPDIR/auth-status-help.out" "Usage: gitcode-mcp auth status" "auth status help"
require_file_contains "$TMPDIR/auth-status-help.out" "env" "auth status help credential sources"
require_file_contains "$TMPDIR/auth-status-help.out" "keychain" "auth status help credential sources"
require_ordered "$TMPDIR/auth-status-help.out" "env" "keychain"
require_file_lacks "$TMPDIR/auth-status-help.out" "dp031-secret-token-value" "auth status help"
require_file_lacks "$TMPDIR/auth-status-help.out" "Authorization:" "auth status help"
require_file_contains "$ARCH" "redacted token preview" "architecture credential redaction"

printf '==> Scenarios 6/7: go test ./... passes offline without live credentials\n'
env -u GITCODE_TOKEN \
  -u GITCODE_E2E_REPO_ID \
  -u GITCODE_LIVE_TEST \
  GITCODE_MCP_CACHE_DIR="$TMPDIR/test-cache" \
  GITCODE_MCP_CONFIG_DIR="$TMPDIR/test-config" \
  go test ./... > "$TMPDIR/go-test.log" 2>&1 || {
    sed -n '1,260p' "$TMPDIR/go-test.log" >&2 || true
    fail "go test ./... failed with live credentials unset"
  }
require_file_contains "$ARCH" "including for \`go test ./...\`" "architecture fixture default"

printf '==> Repository whitespace check\n'
git diff --check

cat > "$TMPDIR/report.json" <<JSON
{
  "scenario_results": {
    "031-docs-task-2-document-provider-and-credential-flow-scenario-1": {"status": "PASS", "details": "architecture Provider Selection documents fixture/live/unavailable modes and one-time resolution"},
    "031-docs-task-2-document-provider-and-credential-flow-scenario-2": {"status": "PASS", "details": "sync --help exposes --live with live behavior"},
    "031-docs-task-2-document-provider-and-credential-flow-scenario-3": {"status": "PASS", "details": "sync help was compared with documented provider predicate"},
    "031-docs-task-2-document-provider-and-credential-flow-scenario-4": {"status": "PASS", "details": "Credential Pipeline documents env to keychain to none and auth status help executed"},
    "031-docs-task-2-document-provider-and-credential-flow-scenario-5": {"status": "PASS", "details": "auth status help listed env/keychain in order and omitted secret-like output"},
    "031-docs-task-2-document-provider-and-credential-flow-scenario-6": {"status": "PASS", "details": "go test ./... ran with GITCODE_TOKEN unset"},
    "031-docs-task-2-document-provider-and-credential-flow-scenario-7": {"status": "PASS", "details": "offline test output passed, supporting documented fixture default"}
  },
  "passed": true,
  "live_validation": false,
  "device_validation": false
}
JSON

cat "$TMPDIR/report.json"
