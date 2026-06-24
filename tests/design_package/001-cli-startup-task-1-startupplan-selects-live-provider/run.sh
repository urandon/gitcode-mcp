#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

export GIT_TERMINAL_PROMPT=0
export GOPROXY=off
export GONOSUMDB='*'
unset GITCODE_TOKEN
unset GITCODE_LIVE_TOKEN
unset GITCODE_TEST_TOKEN
unset GITCODE_LIVE_TEST
unset GITCODE_API_BASE_URL
unset GITCODE_CONFIG
unset GITCODE_MCP_CONFIG

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

SCENARIO_RE='TestCLIStartupPlanSelectsLiveProvider/(SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER|SCN-CLI-LIVE-SYNC-MISSING-CREDENTIAL|SCN-CLI-OFFLINE-SYNC-NO-HTTP|SCN-CLI-LIVE-API-BASE-AUTHORITY|SCN-CLI-DOCTOR-LIVE-JSON-STARTUP-SNAPSHOT)'

go test ./cmd/gitcode-mcp -run "$SCENARIO_RE" -count=1 -v | tee "$TMPDIR/scenario-test.log"

for scenario in \
  SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER \
  SCN-CLI-LIVE-SYNC-MISSING-CREDENTIAL \
  SCN-CLI-OFFLINE-SYNC-NO-HTTP \
  SCN-CLI-LIVE-API-BASE-AUTHORITY \
  SCN-CLI-DOCTOR-LIVE-JSON-STARTUP-SNAPSHOT
 do
  if ! grep -q -- "--- PASS: TestCLIStartupPlanSelectsLiveProvider/${scenario}" "$TMPDIR/scenario-test.log"; then
    printf 'required CLI startup scenario did not pass: %s\n' "$scenario" >&2
    exit 1
  fi
 done

if grep -q 'GITCODE_LIVE_TEST=1' "$TMPDIR/scenario-test.log"; then
  printf 'validation attempted opt-in live integration path\n' >&2
  exit 1
fi

if grep -q 'ISSUE-42\|WIKI-HOME\|test-token' "$TMPDIR/scenario-test.log"; then
  printf 'validation log contains forbidden fixture identifier or token material\n' >&2
  exit 1
fi

go test ./... -count=1
git diff --check
