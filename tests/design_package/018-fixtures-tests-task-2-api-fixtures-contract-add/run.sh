#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

TASK_DIR="tests/design_package/018-fixtures-tests-task-2-api-fixtures-contract-add"
SCENARIOS_FILE="$TASK_DIR/scenarios.md"
VALIDATION_FILE="$TASK_DIR/validation.json"
CONTRACT_TEST_FILE="internal/gitcode/client_test.go"
SANITIZED_TEST_FILE="internal/gitcode/sanitized_fixtures_test.go"

python3 - "$TASK_DIR" <<'PY'
import json
import pathlib
import sys

task_dir = pathlib.Path(sys.argv[1])
scenarios = (task_dir / "scenarios.md").read_text(encoding="utf-8")
required_scenarios = [
    "018-fixtures-tests-task-2-api-fixtures-contract-add-scenario-1",
    "018-fixtures-tests-task-2-api-fixtures-contract-add-scenario-2",
    "018-fixtures-tests-task-2-api-fixtures-contract-add-scenario-3",
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
if set(manifest["covered_outcome_ids"]) != {"outcome-3", "outcome-7"}:
    raise SystemExit("validation.json must cover exactly outcome-3 and outcome-7")
if manifest["covered_decommission_ids"] != []:
    raise SystemExit("validation.json must not cover decommission ids")
if manifest["production_files_modified"] != []:
    raise SystemExit("validation.json must declare no production file modifications")
if not manifest["product_surfaces"]:
    raise SystemExit("validation.json must list product surfaces")
PY

python3 - "$CONTRACT_TEST_FILE" "$SANITIZED_TEST_FILE" <<'PY'
import pathlib
import sys

contract = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
sanitized = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
required_contract_tokens = [
    "func TestContract",
    "httptest.NewServer",
    "ListIssues",
    "GetIssue",
    "ListIssueComments",
    "ListWikiPages",
    "GetWikiPage",
    "func TestTimeout",
    "ErrNetworkUnavailable",
]
missing = [token for token in required_contract_tokens if token not in contract]
if missing:
    raise SystemExit("contract test source missing required evidence tokens: " + ", ".join(missing))
if "func TestSanitizedFixtures" not in sanitized:
    raise SystemExit("sanitized fixture verification test is missing")
PY

required_fixtures=(
  "fixtures/api/v5/repos/example-owner/example-repo/issues.json"
  "fixtures/api/v5/repos/example-owner/example-repo/issues/42.json"
  "fixtures/api/v5/repos/example-owner/example-repo/issues/42/comments.json"
  "fixtures/api/v5/repos/example-owner/example-repo/wiki/pages.json"
  "fixtures/api/v5/repos/example-owner/example-repo/wiki/Home.json"
)

for fixture in "${required_fixtures[@]}"; do
  if [[ ! -s "$fixture" ]]; then
    printf 'missing or empty required fixture: %s\n' "$fixture" >&2
    exit 1
  fi
done

python3 - "${required_fixtures[@]}" <<'PY'
import json
import pathlib
import re
import sys

for fixture in sys.argv[1:]:
    path = pathlib.Path(fixture)
    rel = path.as_posix()
    text = path.read_text(encoding="utf-8")
    try:
        json.loads(text)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"fixture is not valid JSON: {rel}: {exc}") from exc
    for token in ["Authorization", "raw-owner", "raw-repo", "raw-project", "gitcode.example.invalid"]:
        if token in rel or token in text:
            raise SystemExit(f"fixture contains forbidden token {token!r}: {rel}")
    for host in re.findall(r"(?i)\b(?:[a-z0-9-]+\.)+[a-z]{2,}\b", text):
        if host != "api.example.com":
            raise SystemExit(f"fixture contains disallowed hostname {host!r}: {rel}")
PY

contract_output_file="$(mktemp)"
sanitize_output_file="$(mktemp)"
cleanup() {
  rm -f "$contract_output_file" "$sanitize_output_file"
}
trap cleanup EXIT

if ! go test ./internal/gitcode/... -run '^(TestContract|TestTimeout)$' -count=1 -v >"$contract_output_file" 2>&1; then
  while IFS= read -r line; do printf '%s\n' "$line"; done <"$contract_output_file"
  exit 1
fi
while IFS= read -r line; do printf '%s\n' "$line"; done <"$contract_output_file"

python3 - "$contract_output_file" <<'PY'
import pathlib
import sys

output = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
required = [
    "=== RUN   TestContract",
    "=== RUN   TestTimeout",
    "--- PASS: TestContract",
    "--- PASS: TestTimeout",
]
missing = [item for item in required if item not in output]
if missing:
    raise SystemExit("missing contract test evidence: " + ", ".join(missing))
PY

if ! go test ./internal/gitcode/... -run '^TestSanitizedFixtures$' -count=1 -v >"$sanitize_output_file" 2>&1; then
  while IFS= read -r line; do printf '%s\n' "$line"; done <"$sanitize_output_file"
  exit 1
fi
while IFS= read -r line; do printf '%s\n' "$line"; done <"$sanitize_output_file"

python3 - "$sanitize_output_file" <<'PY'
import pathlib
import sys

output = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
required = ["=== RUN   TestSanitizedFixtures", "--- PASS: TestSanitizedFixtures"]
missing = [item for item in required if item not in output]
if missing:
    raise SystemExit("missing sanitized fixture evidence: " + ", ".join(missing))
PY

go test ./internal/gitcode/... -run '^TestContract$' -count=1

printf '018 fixtures contract validation passed\n'
