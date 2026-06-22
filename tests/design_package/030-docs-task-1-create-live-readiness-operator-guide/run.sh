#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/030-docs-task-1-create-live-readiness-operator-guide"
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

require_no_secret() {
  local file="$1"
  local secret="dp030-secret-token-value"
  if python3 - "$file" "$secret" <<'PY'
import sys
from pathlib import Path
text = Path(sys.argv[1]).read_text(errors="replace")
needle = sys.argv[2]
raise SystemExit(0 if needle in text else 1)
PY
  then
    fail "$file leaked deterministic fake secret"
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

GUIDE="docs/live-readiness.md"
[[ -f "$GUIDE" ]] || fail "docs/live-readiness.md is missing"

printf '==> Scenario 1/2: bind help and auth help match guide sections 2 and 3\n'
require_file_contains "$GUIDE" 'gitcode-mcp bind --repo-owner "YOUR_OWNER" --repo "YOUR_REPO"' "live-readiness bind docs"
require_file_contains "$GUIDE" "gitcode-mcp auth status" "live-readiness auth status docs"
run_expect_success "$TMPDIR/bind-help.out" "$BIN" bind --help
run_expect_success "$TMPDIR/auth-status-help.out" "$BIN" auth status --help
require_file_contains "$TMPDIR/bind-help.out" "Usage: gitcode-mcp bind" "bind help"
require_file_contains "$TMPDIR/bind-help.out" "--repo-owner" "bind help"
require_file_contains "$TMPDIR/bind-help.out" "--repo" "bind help"
require_file_contains "$TMPDIR/auth-status-help.out" "Usage: gitcode-mcp auth status [--live] [--owner OWNER] [--repo REPO] [--format FORMAT]" "auth status help"
require_file_contains "$TMPDIR/auth-status-help.out" "Report token source, credential state, and optional auth probe." "auth status help"
require_file_contains "$TMPDIR/auth-status-help.out" "--live              probe GitCode API with token" "auth status help"
require_file_contains "$TMPDIR/auth-status-help.out" "--owner OWNER       repository owner (for auth probe)" "auth status help"
require_file_contains "$TMPDIR/auth-status-help.out" "--repo REPO         repository id (for auth probe)" "auth status help"
require_file_contains "$TMPDIR/auth-status-help.out" "--format FORMAT     output format (text, json)" "auth status help"

printf '==> Scenario 3/4/5: auth status without token reports documented sources and no secrets\n'
env -u GITCODE_TOKEN \
  GITCODE_MCP_CACHE_DIR="$TMPDIR/auth-cache" \
  GITCODE_MCP_CONFIG_DIR="$TMPDIR/auth-config" \
  "$BIN" auth status > "$TMPDIR/auth-no-token.out" 2>&1
require_file_contains "$TMPDIR/auth-no-token.out" "token_present: false" "auth status no token"
require_file_contains "$TMPDIR/auth-no-token.out" "available_sources:" "auth status no token"
require_file_contains "$TMPDIR/auth-no-token.out" "env" "auth status no token"
require_file_contains "$TMPDIR/auth-no-token.out" "keychain" "auth status no token"
require_file_contains "$TMPDIR/auth-no-token.out" "none" "auth status no token"
require_ordered "$GUIDE" "GITCODE_TOKEN" "keychain" "none"
require_no_secret "$TMPDIR/auth-no-token.out"

printf '==> Scenario 6/7: sync help exposes command-local --live matching guide section 4\n'
require_file_contains "$GUIDE" "gitcode-mcp sync --live" "live-readiness sync docs"
run_expect_success "$TMPDIR/sync-help.out" "$BIN" sync --help
require_file_contains "$TMPDIR/sync-help.out" "Usage: gitcode-mcp sync" "sync help"
require_file_contains "$TMPDIR/sync-help.out" "--live" "sync help"
require_file_contains "$TMPDIR/sync-help.out" "live" "sync help"

printf '==> Scenario 8/9: create-issue help matches guide section 5\n'
require_file_contains "$GUIDE" "gitcode-mcp create-issue --live --idempotency-key \"ik-001\" --title \"Test\"" "live-readiness create-issue docs"
run_expect_success "$TMPDIR/create-issue-help.out" "$BIN" create-issue --help
require_file_contains "$TMPDIR/create-issue-help.out" "Usage: gitcode-mcp create-issue" "create-issue help"
require_file_contains "$TMPDIR/create-issue-help.out" "Create a new issue. Requires exactly one of --dry-run or --live." "create-issue help"
require_file_contains "$TMPDIR/create-issue-help.out" "--title TITLE       issue title (required)" "create-issue help"
require_file_contains "$TMPDIR/create-issue-help.out" "--body BODY         issue body" "create-issue help"
require_file_contains "$TMPDIR/create-issue-help.out" "--idempotency-key KEY  idempotency key" "create-issue help"
require_file_contains "$TMPDIR/create-issue-help.out" "--dry-run           validate without mutation" "create-issue help"
require_file_contains "$TMPDIR/create-issue-help.out" "--live              execute live write" "create-issue help"

printf '==> Scenario 10/11: doctor no-binding diagnostic suggests bind command\n'
require_file_contains "$GUIDE" "gitcode-mcp doctor" "live-readiness doctor docs"
require_file_contains "$GUIDE" "bind" "live-readiness doctor docs"
env -u GITCODE_TOKEN \
  GITCODE_MCP_CACHE_DIR="$TMPDIR/doctor-cache" \
  GITCODE_MCP_CONFIG_DIR="$TMPDIR/doctor-config" \
  "$BIN" doctor > "$TMPDIR/doctor-no-binding.out" 2>&1
require_file_contains "$TMPDIR/doctor-no-binding.out" "no_repo_bound" "doctor no binding"
require_file_contains "$TMPDIR/doctor-no-binding.out" "bind" "doctor no binding"
require_no_secret "$TMPDIR/doctor-no-binding.out"

printf '==> Offline regression checks\n'
go test ./... > "$TMPDIR/go-test.log" 2>&1 || {
  cat "$TMPDIR/go-test.log" >&2
  fail "go test ./... failed"
}
git diff --check

cat > "$TMPDIR/report.json" <<JSON
{
  "scenario_results": {
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-1": {"status": "PASS", "details": "bind help and auth status help executed in documented sequence"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-2": {"status": "PASS", "details": "help flags and descriptions matched docs/live-readiness.md sections 2 and 3"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-3": {"status": "PASS", "details": "auth status ran with GITCODE_TOKEN unset"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-4": {"status": "PASS", "details": "auth output listed env/keychain/none sources and no token"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-5": {"status": "PASS", "details": "auth stdout evidence matched documented credential order"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-6": {"status": "PASS", "details": "sync help exposed command-local --live"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-7": {"status": "PASS", "details": "sync help evidence matched documented live sync behavior"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-8": {"status": "PASS", "details": "create-issue help executed"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-9": {"status": "PASS", "details": "create-issue help contained documented write flags and descriptions"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-10": {"status": "PASS", "details": "doctor ran against isolated no-binding cache"},
    "030-docs-task-1-create-live-readiness-operator-guide-scenario-11": {"status": "PASS", "details": "doctor output included no_repo_bound and bind suggestion"}
  },
  "passed": true,
  "live_validation": false,
  "device_validation": false
}
JSON

cat "$TMPDIR/report.json"
