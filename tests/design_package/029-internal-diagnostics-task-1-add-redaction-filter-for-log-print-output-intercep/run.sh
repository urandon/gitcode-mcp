#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/029-internal-diagnostics-task-1-add-redaction-filter-for-log-print-output-intercep"
TMPDIR="$(mktemp -d "$SCENARIO_DIR/tmp.XXXXXX")"
SERVER_PID=""
cleanup() {
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -f "$SCENARIO_DIR/redaction_runtime_test.go"
  rm -rf "$TMPDIR"
}
trap cleanup EXIT

cd "$ROOT"

TOKEN="dp029-secret-token-value"
OWNER="dp029-private-owner"
REPO="dp029-private-repo"
PRIVATE_HOST="dp029.private.example.invalid"
RAW_BODY="dp029-raw-api-response-body"
COOKIE_VALUE="dp029-cookie-secret"
AUTH_VALUE="Bearer $TOKEN"
FORBIDDEN=("$TOKEN" "$OWNER" "$REPO" "$PRIVATE_HOST" "$RAW_BODY" "$COOKIE_VALUE" "$AUTH_VALUE")

fail() {
  printf 'VALIDATION FAILURE: %s\n' "$*" >&2
  exit 1
}

assert_no_forbidden() {
  local file="$1"
  local label="$2"
  for forbidden in "${FORBIDDEN[@]}"; do
    if [[ -n "$forbidden" ]] && python3 - "$file" "$forbidden" <<'PY'
import sys
from pathlib import Path
path = Path(sys.argv[1])
needle = sys.argv[2]
raise SystemExit(0 if needle in path.read_text(errors="replace") else 1)
PY
    then
      fail "$label leaked forbidden substring: $forbidden"
    fi
  done
}

assert_has_redacted() {
  local file="$1"
  local label="$2"
  if ! python3 - "$file" <<'PY'
import sys
from pathlib import Path
raise SystemExit(0 if "[REDACTED]" in Path(sys.argv[1]).read_text(errors="replace") else 1)
PY
  then
    fail "$label did not contain [REDACTED] marker"
  fi
}

printf '==> Scenario 1: production redaction and CLI diagnostics tests\n'
go test ./internal/diagnostics ./internal/gitcode ./internal/config ./internal/doctor ./internal/cli -count=1 -run 'TestFilterRedactsDiagnosticSecrets|TestFilterRedactsHeadersURLJSONAndWriter|TestConfigAuthCommandsRedactedUX|TestRuntimeAuditDoctorCommand|TestDoctorCommandFull' -v > "$TMPDIR/go-redaction-tests.log" 2>&1 || {
  cat "$TMPDIR/go-redaction-tests.log" >&2
  fail "Go redaction/CLI diagnostic tests failed"
}
assert_no_forbidden "$TMPDIR/go-redaction-tests.log" "Go redaction test output"

printf '==> Scenario 1: e2e package compiles offline and skips without live env\n'
env -u GITCODE_TOKEN -u GITCODE_E2E_OWNER -u GITCODE_E2E_REPO -u GITCODE_E2E_REPO_ID -u GITCODE_E2E_API_BASE_URL -u GITCODE_E2E_BASE_URL \
  go test -run TestE2ELiveTwoCache -tags=e2e ./internal/e2e/ -count=1 -v > "$TMPDIR/e2e-tests.log" 2>&1 || {
    cat "$TMPDIR/e2e-tests.log" >&2
    fail "e2e package build/skip validation failed"
  }
assert_no_forbidden "$TMPDIR/e2e-tests.log" "e2e test output"

printf '==> Scenario 1: generated runtime test exercises writer/header/url/body filter\n'
cat > "$TMPDIR/redaction_runtime_test.go" <<'GO'
package dp029validation

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"gitcode-mcp/internal/diagnostics"
)

func TestDP029RuntimeRedactionSurfaces(t *testing.T) {
	token := "dp029-secret-token-value"
	owner := "dp029-private-owner"
	repo := "dp029-private-repo"
	rawBody := "dp029-raw-api-response-body"
	cookie := "dp029-cookie-secret"
	filter := diagnostics.NewFilter(token, owner, repo, "dp029.private.example.invalid", rawBody, cookie).WithApprovedHosts("127.0.0.1")
	var buf bytes.Buffer
	writer := filter.RedactedWriter(&buf)
	_, err := writer.Write([]byte("Authorization: Bearer " + token + "\nCookie: session=" + cookie + "\nhttps://dp029.private.example.invalid/api?access_token=" + token + "\n" + rawBody + " for " + owner + "/" + repo))
	if err != nil {
		t.Fatal(err)
	}
	url := filter.RedactURL("https://dp029.private.example.invalid/api?access_token=" + token + "&repo=" + repo)
	headers := filter.RedactHeaders(http.Header{"Authorization": {"Bearer " + token}, "Cookie": {"session=" + cookie}, "X-Repo": {owner + "/" + repo}})
	body := string(filter.RedactJSONBody([]byte(`{"authorization":"Bearer dp029-secret-token-value","owner":"dp029-private-owner","repo":"dp029-private-repo","body":"dp029-raw-api-response-body"}`)))
	summary := filter.RawAPIResponseSummary(401, []byte(rawBody))
	combined := buf.String() + url + body + summary + strings.Join(headers["Authorization"], "") + strings.Join(headers["Cookie"], "") + strings.Join(headers["X-Repo"], "")
	for _, forbidden := range []string{token, owner, repo, rawBody, cookie, "Bearer " + token, "dp029.private.example.invalid"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("redaction runtime output leaked %q: %s", forbidden, combined)
		}
	}
	if !strings.Contains(combined, diagnostics.Redacted) {
		t.Fatalf("redaction runtime output missing redaction marker: %s", combined)
	}
}
GO
cp "$TMPDIR/redaction_runtime_test.go" "$SCENARIO_DIR/redaction_runtime_test.go"
go test ./tests/design_package/029-internal-diagnostics-task-1-add-redaction-filter-for-log-print-output-intercep -count=1 -run TestDP029RuntimeRedactionSurfaces -v > "$TMPDIR/runtime-redaction.log" 2>&1 || {
  cat "$TMPDIR/runtime-redaction.log" >&2
  fail "generated runtime redaction test failed"
}
assert_no_forbidden "$TMPDIR/runtime-redaction.log" "generated runtime redaction test output"

printf '==> Scenario 1/2: build real CLI and exercise auth, doctor, and live error diagnostics offline\n'
BIN="$TMPDIR/gitcode-mcp"
go build -o "$BIN" ./cmd/gitcode-mcp

cat > "$TMPDIR/stub_server.py" <<'PY'
import http.server
import socketserver
import sys

raw_body = sys.argv[1]
cookie_value = sys.argv[2]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(401)
        self.send_header("Content-Type", "application/json")
        self.send_header("Set-Cookie", "session=" + cookie_value)
        self.end_headers()
        self.wfile.write(("{\"message\":\"" + raw_body + "\",\"authorization\":\"" + self.headers.get("Authorization", "missing") + "\"}").encode())
    def log_message(self, fmt, *args):
        pass

with socketserver.TCPServer(("127.0.0.1", 0), Handler) as httpd:
    print(httpd.server_address[1], flush=True)
    httpd.serve_forever()
PY
python3 "$TMPDIR/stub_server.py" "$RAW_BODY" "$COOKIE_VALUE" > "$TMPDIR/stub.port" 2> "$TMPDIR/stub.err" &
SERVER_PID=$!
for _ in {1..50}; do
  [[ -s "$TMPDIR/stub.port" ]] && break
  sleep 0.1
done
[[ -s "$TMPDIR/stub.port" ]] || fail "local stub server did not start"
PORT="$(cat "$TMPDIR/stub.port")"
BASE_URL="http://127.0.0.1:$PORT/api/v5?access_token=$TOKEN&private_host=$PRIVATE_HOST"

COMMON_ENV=(
  "GITCODE_TOKEN=$TOKEN"
  "GITCODE_E2E_OWNER=$OWNER"
  "GITCODE_E2E_REPO=$REPO"
  "GITCODE_E2E_REPO_ID=$OWNER/$REPO"
  "GITCODE_E2E_API_BASE_URL=$BASE_URL"
  "GITCODE_E2E_BASE_URL=$BASE_URL"
  "GITCODE_API_URL=$BASE_URL"
)

env "${COMMON_ENV[@]}" "$BIN" auth status > "$TMPDIR/auth-status.out" 2>&1
env "${COMMON_ENV[@]}" "$BIN" doctor --format json > "$TMPDIR/doctor-json.out" 2>&1
set +e
env "${COMMON_ENV[@]}" "$BIN" auth status --live --owner "$OWNER" --repo "$REPO" > "$TMPDIR/auth-live.out" 2>&1
AUTH_LIVE_CODE=$?
set -e
if [[ "$AUTH_LIVE_CODE" -eq 0 ]]; then
  fail "auth status --live unexpectedly succeeded against 401 stub"
fi
cat "$TMPDIR/auth-status.out" "$TMPDIR/doctor-json.out" "$TMPDIR/auth-live.out" > "$TMPDIR/cli-combined.out"
assert_no_forbidden "$TMPDIR/cli-combined.out" "CLI diagnostics output"
assert_has_redacted "$TMPDIR/cli-combined.out" "CLI diagnostics output"
if ! python3 - "$TMPDIR/cli-combined.out" <<'PY'
import sys
from pathlib import Path
text = Path(sys.argv[1]).read_text(errors="replace")
required = ["credential_source: env:GITCODE_TOKEN", '"token_present": true', "auth"]
raise SystemExit(0 if all(item in text for item in required) else 1)
PY
then
  fail "CLI diagnostics output did not include expected auth/doctor status evidence"
fi
kill "$SERVER_PID" >/dev/null 2>&1 || true
wait "$SERVER_PID" 2>/dev/null || true

printf '==> Full offline regression suite\n'
go test ./... > "$TMPDIR/full-go-test.log" 2>&1 || {
  cat "$TMPDIR/full-go-test.log" >&2
  fail "go test ./... failed"
}
assert_no_forbidden "$TMPDIR/full-go-test.log" "full Go test output"

git diff --check

cat > "$TMPDIR/report.json" <<JSON
{
  "scenario_results": {
    "029-internal-diagnostics-task-1-add-redaction-filter-for-log-print-output-intercep-scenario-1": {
      "status": "PASS",
      "details": "diagnostics package tests, CLI auth/doctor/error paths, local stub live auth failure, and e2e build/skip output contained no forbidden raw token, private URL/coordinate, Authorization header value, cookie, or raw API body text"
    },
    "029-internal-diagnostics-task-1-add-redaction-filter-for-log-print-output-intercep-scenario-2": {
      "status": "PASS",
      "details": "runtime outputs contained [REDACTED] and excluded the deterministic fake token value"
    }
  },
  "passed": true,
  "live_validation": false,
  "device_validation": false
}
JSON
cat "$TMPDIR/report.json"
