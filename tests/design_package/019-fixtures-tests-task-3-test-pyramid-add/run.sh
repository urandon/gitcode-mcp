#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

TASK_DIR="tests/design_package/019-fixtures-tests-task-3-test-pyramid-add"
SCENARIOS_FILE="$TASK_DIR/scenarios.md"
VALIDATION_FILE="$TASK_DIR/validation.json"
TESTNET_FILE="internal/testnet/testnet.go"
TESTNET_TEST_FILE="internal/testnet/testnet_test.go"
GITCODE_TEST_FILE="internal/gitcode/client_test.go"
SERVICE_TEST_FILE="internal/service/service_test.go"

python3 - "$TASK_DIR" <<'PY'
import json
import pathlib
import sys

task_dir = pathlib.Path(sys.argv[1])
scenarios = (task_dir / "scenarios.md").read_text(encoding="utf-8")
required_scenarios = [
    "019-fixtures-tests-task-3-test-pyramid-add-scenario-1",
    "019-fixtures-tests-task-3-test-pyramid-add-scenario-2",
    "019-fixtures-tests-task-3-test-pyramid-add-scenario-3",
    "019-fixtures-tests-task-3-test-pyramid-add-scenario-4",
]
missing = [scenario for scenario in required_scenarios if scenario not in scenarios]
if missing:
    raise SystemExit("scenarios.md missing required scenario ids: " + ", ".join(missing))

manifest = json.loads((task_dir / "validation.json").read_text(encoding="utf-8"))
required_keys = [
    "covered_outcome_ids",
    "covered_decommission_ids",
    "product_surfaces",
    "evidence_type",
    "freshness",
    "mocks_used",
    "production_files_modified",
]
missing_keys = [key for key in required_keys if key not in manifest]
if missing_keys:
    raise SystemExit("validation.json missing required keys: " + ", ".join(missing_keys))
if set(manifest["covered_outcome_ids"]) != {"outcome-8"}:
    raise SystemExit("validation.json must cover exactly outcome-8")
if manifest["covered_decommission_ids"] != []:
    raise SystemExit("validation.json must not cover decommission ids")
if manifest["production_files_modified"] != []:
    raise SystemExit("validation.json must declare no production file modifications")
if not manifest["product_surfaces"]:
    raise SystemExit("validation.json must list product surfaces")
PY

python3 - "$TESTNET_FILE" "$TESTNET_TEST_FILE" "$GITCODE_TEST_FILE" "$SERVICE_TEST_FILE" <<'PY'
import pathlib
import re
import sys

testnet = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
testnet_test = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
gitcode_test = pathlib.Path(sys.argv[3]).read_text(encoding="utf-8")
service_test = pathlib.Path(sys.argv[4]).read_text(encoding="utf-8")

required_testnet_tokens = [
    "var ErrExternalNetwork",
    "func NoExternalNetwork",
    "func GuardedTransport",
    "func RequireLiveIntegration",
    "testing.Short()",
    "GITCODE_TEST_TOKEN",
]
missing = [token for token in required_testnet_tokens if token not in testnet]
if missing:
    raise SystemExit("testnet helper missing required tokens: " + ", ".join(missing))

required_guard_test_tokens = [
    "func TestNoExternalNetwork",
    "NoExternalNetwork(t)",
    "ErrExternalNetwork",
    "httptest.NewServer",
]
missing = [token for token in required_guard_test_tokens if token not in testnet_test]
if missing:
    raise SystemExit("network guard evidence test missing required tokens: " + ", ".join(missing))

if "func TestIntegrationLiveGitCodeGate" not in gitcode_test or "testnet.RequireLiveIntegration(t)" not in gitcode_test:
    raise SystemExit("live integration test must call testnet.RequireLiveIntegration(t)")
match = re.search(r"func TestIntegrationLiveGitCodeGate\(t \*testing\.T\) \{(?P<body>.*?)\n\}", gitcode_test, re.S)
if not match:
    raise SystemExit("could not locate TestIntegrationLiveGitCodeGate body")
body = match.group("body")
if "integration-token" in body:
    raise SystemExit("live integration body uses a hard-coded placeholder token instead of GITCODE_TEST_TOKEN")
if "os.Getenv(\"GITCODE_TEST_TOKEN\")" not in body:
    raise SystemExit("live integration body must read GITCODE_TEST_TOKEN for live client credentials")

if "func TestGoldenExport" not in service_test or "testdata/golden_export.md" not in service_test:
    raise SystemExit("golden export test evidence is missing")
PY

run_and_capture() {
  local outfile="$1"
  shift
  if ! "$@" >"$outfile" 2>&1; then
    while IFS= read -r line; do printf '%s\n' "$line"; done <"$outfile"
    exit 1
  fi
  while IFS= read -r line; do printf '%s\n' "$line"; done <"$outfile"
}

short_output_file="$(mktemp)"
net_output_file="$(mktemp)"
integration_output_file="$(mktemp)"
short_integration_output_file="$(mktemp)"
golden_output_file="$(mktemp)"
cleanup() {
  rm -f "$short_output_file" "$net_output_file" "$integration_output_file" "$short_integration_output_file" "$golden_output_file"
}
trap cleanup EXIT

start_seconds="$(python3 - <<'PY'
import time
print(time.monotonic())
PY
)"
run_and_capture "$short_output_file" env -u GITCODE_TEST_TOKEN go test ./... -short -count=1
end_seconds="$(python3 - <<'PY'
import time
print(time.monotonic())
PY
)"
python3 - "$start_seconds" "$end_seconds" "$short_output_file" <<'PY'
import pathlib
import sys

start = float(sys.argv[1])
end = float(sys.argv[2])
output = pathlib.Path(sys.argv[3]).read_text(encoding="utf-8")
if end - start >= 10:
    raise SystemExit(f"short test suite exceeded 10s budget: {end - start:.2f}s")
if "FAIL" in output:
    raise SystemExit("short test output contains FAIL")
PY

run_and_capture "$net_output_file" go test ./internal/testnet/... -run '^TestNoExternalNetwork$' -count=1 -v
python3 - "$net_output_file" <<'PY'
import pathlib
import sys
output = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
required = ["=== RUN   TestNoExternalNetwork", "--- PASS: TestNoExternalNetwork"]
missing = [item for item in required if item not in output]
if missing:
    raise SystemExit("missing network guard evidence: " + ", ".join(missing))
PY

run_and_capture "$integration_output_file" env -u GITCODE_TEST_TOKEN go test ./... -run Integration -count=1 -v
python3 - "$integration_output_file" <<'PY'
import pathlib
import sys
output = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
if "FAIL" in output:
    raise SystemExit("integration-without-token output contains FAIL")
if "GITCODE_TEST_TOKEN unset" not in output:
    raise SystemExit("integration-without-token output lacks live skip evidence")
if "=== RUN   TestIntegrationLiveGitCodeGate" not in output:
    raise SystemExit("integration-without-token output lacks live gate test execution")
PY

run_and_capture "$short_integration_output_file" env GITCODE_TEST_TOKEN=placeholder go test ./... -short -run Integration -count=1 -v
python3 - "$short_integration_output_file" <<'PY'
import pathlib
import sys
output = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
if "FAIL" in output:
    raise SystemExit("short integration output contains FAIL")
if "live integration skipped in short mode" not in output:
    raise SystemExit("short integration output lacks short-mode live skip evidence")
PY

run_and_capture "$golden_output_file" go test ./internal/service/... -run '^TestGoldenExport$' -count=1 -v
python3 - "$golden_output_file" <<'PY'
import pathlib
import sys
output = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
required = ["=== RUN   TestGoldenExport", "--- PASS: TestGoldenExport"]
missing = [item for item in required if item not in output]
if missing:
    raise SystemExit("missing golden export evidence: " + ", ".join(missing))
PY

printf '019 test pyramid validation passed\n'
