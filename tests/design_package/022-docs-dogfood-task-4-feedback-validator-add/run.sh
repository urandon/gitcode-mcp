#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
PASS_FEEDBACK="${REPO_ROOT}/project/dogfood/testdata/feedback-pass.md"
FAIL_FEEDBACK="${REPO_ROOT}/project/dogfood/testdata/feedback-fail.md"
FEEDBACK_TEMPLATE="${REPO_ROOT}/project/dogfood/feedback.md"
CHECK="${REPO_ROOT}/project/dogfood/check-dogfood-feedback"
ALLOWLIST="${REPO_ROOT}/project/dogfood/fixture-allowlist.txt"

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

failures=0

fail() {
  printf "${RED}FAIL:${NC} %s\n" "$*"
  failures=$((failures + 1))
}

pass_msg() {
  printf "${GREEN}PASS:${NC} %s\n" "$*"
}

# === Validation Contract Self-Check ===
echo "=== Validation Contract Self-Check ==="

python3 - "${SCRIPT_DIR}" <<'PY'
import json
import pathlib
import sys

task_dir = pathlib.Path(sys.argv[1])
scenarios = (task_dir / "scenarios.md").read_text(encoding="utf-8")
required_scenarios = [
    "022-docs-dogfood-task-4-feedback-validator-add-scenario-1",
    "022-docs-dogfood-task-4-feedback-validator-add-scenario-2",
    "022-docs-dogfood-task-4-feedback-validator-add-scenario-3",
    "022-docs-dogfood-task-4-feedback-validator-add-scenario-4",
]
missing = [s for s in required_scenarios if s not in scenarios]
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
if set(manifest["covered_outcome_ids"]) != {"outcome-16"}:
    raise SystemExit("validation.json must cover exactly outcome-16")
if manifest["covered_decommission_ids"] != []:
    raise SystemExit("validation.json must not cover decommission ids")
if manifest["production_files_modified"] != []:
    raise SystemExit("validation.json must declare no production file modifications")
if not manifest["product_surfaces"]:
    raise SystemExit("validation.json must list non-empty product_surfaces")
if manifest["mocks_used"] != False:
    raise SystemExit("validation.json must declare mocks_used=false")
PY

pass_msg "Validation contract self-check passed"
echo ""

# Prerequisites
if [[ ! -x "${CHECK}" ]]; then
  fail "check-dogfood-feedback is not executable or not found at ${CHECK}"
  exit 1
fi

if [[ ! -f "${ALLOWLIST}" ]]; then
  fail "fixture-allowlist.txt not found at ${ALLOWLIST}"
  exit 1
fi

if [[ ! -f "${PASS_FEEDBACK}" ]]; then
  fail "feedback-pass.md not found at ${PASS_FEEDBACK}"
  exit 1
fi

if [[ ! -f "${FAIL_FEEDBACK}" ]]; then
  fail "feedback-fail.md not found at ${FAIL_FEEDBACK}"
  exit 1
fi

if [[ ! -f "${FEEDBACK_TEMPLATE}" ]]; then
  fail "feedback.md template not found at ${FEEDBACK_TEMPLATE}"
  exit 1
fi

# Scenario 1+2: Validator exits 0 on sanitized feedback template.
# Verifies the stable command and public-safe feedback artifact surface.
echo "=== Scenario 1+2: Sanitized feedback template ==="
if output=$("${CHECK}" "${FEEDBACK_TEMPLATE}" 2>&1); then
  pass_msg "feedback.md template passed validation (exit 0) — public-safe artifact confirmed"
else
  fail "feedback.md template failed validation (exit != 0): ${output}"
fi

echo ""

# Scenario 4: Pass fixture must exit 0 with zero fatal findings.
echo "=== Scenario 4a: Pass fixture ==="
if pass_output=$("${CHECK}" "${PASS_FEEDBACK}" 2>&1); then
  if echo "${pass_output}" | grep -q "PASS"; then
    pass_msg "feedback-pass.md validated with PASS"
  else
    pass_msg "feedback-pass.md validated successfully (exit 0)"
  fi
else
  fail "feedback-pass.md failed validation: ${pass_output}"
fi

echo ""

# Scenario 3+4: Fail fixture must exit nonzero with categorized line-referenced findings.
echo "=== Scenario 4b/Scenario 3: Fail fixture ==="
fail_output=""
if fail_output=$("${CHECK}" "${FAIL_FEEDBACK}" 2>&1); then
  fail "feedback-fail.md should have failed with fatal findings but exited 0"
else
  exit_code=$?
  # Verify specific categories are present
  check_category() {
    local category="$1"
    local label="$2"
    if echo "${fail_output}" | grep -q "${category}"; then
      pass_msg "Fail fixture correctly reports ${label} (${category})"
    else
      fail "Fail fixture missing ${label} finding (${category})"
    fi
  }

  check_category "bearer_token" "bearer token"
  check_category "auth_header" "authorization header"
  check_category "cookie_header" "cookie header"
  check_category "private_path" "private path"
  check_category "private_hostname" "private host"
  check_category "raw_response" "raw response"
  check_category "unallowlisted_id" "non-allowlisted identifier"

  # Verify line references are present (format: path:line:category:message)
  if echo "${fail_output}" | grep -qE "^/|project/"; then
    pass_msg "Fail fixture contains line-referenced findings (path:line:category:message format)"
  else
    fail "Fail fixture findings missing line references"
  fi

  # Verify findings are on the expected lines
  if echo "${fail_output}" | grep -q "$(basename ${FAIL_FEEDBACK}):8:api_token:"; then
    pass_msg "Token finding on correct line (line 8)"
  else
    fail "Token finding not on expected line"
  fi

  if echo "${fail_output}" | grep -q "$(basename ${FAIL_FEEDBACK}):9:bearer_token:"; then
    pass_msg "Bearer token finding on correct line (line 9)"
  else
    fail "Bearer token finding not on expected line"
  fi

  if echo "${fail_output}" | grep -q "$(basename ${FAIL_FEEDBACK}):10:cookie_header:"; then
    pass_msg "Cookie header finding on correct line (line 10)"
  else
    fail "Cookie header finding not on expected line"
  fi

  if echo "${fail_output}" | grep -q "$(basename ${FAIL_FEEDBACK}):12:private_path:.*macOS"; then
    pass_msg "macOS private path finding on correct line (line 12)"
  else
    fail "macOS private path finding not on expected line"
  fi

  if echo "${fail_output}" | grep -q "$(basename ${FAIL_FEEDBACK}):13:private_path:.*Linux"; then
    pass_msg "Linux private path finding on correct line (line 13)"
  else
    fail "Linux private path finding not on expected line"
  fi

  if echo "${fail_output}" | grep -q "$(basename ${FAIL_FEEDBACK}):26:private_hostname:"; then
    pass_msg "Private hostname finding on correct line (line 26)"
  else
    fail "Private hostname finding not on expected line"
  fi

  if echo "${fail_output}" | grep -q "$(basename ${FAIL_FEEDBACK}):27:private_path:.*Windows"; then
    pass_msg "Windows profile path finding on correct line (line 27)"
  else
    fail "Windows profile path finding not on expected line"
  fi

  if echo "${fail_output}" | grep -q "$(basename ${FAIL_FEEDBACK}):28:raw_response:"; then
    pass_msg "Raw response finding on correct line (line 28)"
  else
    fail "Raw response finding not on expected line"
  fi

  if echo "${fail_output}" | grep -q "$(basename ${FAIL_FEEDBACK}):28:unallowlisted_id:"; then
    pass_msg "Unallowlisted identifier finding on correct line (line 28)"
  else
    fail "Unallowlisted identifier finding not on expected line"
  fi

  if [[ ${exit_code} -ne 0 ]]; then
    pass_msg "Fail fixture correctly exited nonzero (${exit_code})"
  fi
fi

echo ""

# Final report
echo "=== Summary ==="
if [[ ${failures} -eq 0 ]]; then
  printf "${GREEN}All validations passed.${NC}\n"
  exit 0
else
  printf "${RED}%d validation(s) failed.${NC}\n" "${failures}"
  exit 1
fi
