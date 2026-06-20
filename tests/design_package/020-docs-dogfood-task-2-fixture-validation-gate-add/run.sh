#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Walk up from tests/design_package/020-docs-dogfood-task-2-fixture-validation-gate-add to repo root
REPO_ROOT="${SCRIPT_DIR}"
while [[ "$(basename "${REPO_ROOT}")" != "gitcode-mcp" ]]; do
  REPO_ROOT="$(dirname "${REPO_ROOT}")"
  if [[ "${REPO_ROOT}" == "/" ]]; then
    echo "ERROR: could not find gitcode-mcp repo root" >&2
    exit 1
  fi
done
DOGFOOD_DIR="${REPO_ROOT}/project/dogfood"
FAILURES=0

red()  { printf '\033[31m%s\033[0m\n' "$@"; }
green(){ printf '\033[32m%s\033[0m\n' "$@"; }

fail() {
  red "FAIL: $*"
  FAILURES=$((FAILURES + 1))
}

pass() {
  green "PASS: $*"
}

# ---- pre-flight ----

echo "=== validate-fixtures.sh validation ==="
echo "REPO_ROOT=${REPO_ROOT}"
echo ""

VSCRIPT="${DOGFOOD_DIR}/validate-fixtures.sh"
SAFETY_LIB="${DOGFOOD_DIR}/lib/safety.sh"
MANIFEST="${DOGFOOD_DIR}/docs-smoke.commands"

if [[ ! -f "${VSCRIPT}" ]]; then
  fail "validate-fixtures.sh does not exist at ${VSCRIPT}"
  exit 1
fi

if [[ ! -x "${VSCRIPT}" ]]; then
  fail "validate-fixtures.sh is not executable"
  exit 1
fi
pass "validate-fixtures.sh exists and is executable"

if [[ ! -f "${SAFETY_LIB}" ]]; then
  fail "safety.sh lib not found at ${SAFETY_LIB}"
  exit 1
fi
pass "Safety lib exists"

if [[ ! -f "${MANIFEST}" ]]; then
  fail "docs-smoke.commands manifest not found"
  exit 1
fi

# ---- scenario 1: offline run without credentials ----

echo ""
echo "--- Scenario 1: offline run without credentials ---"

TRANSCRIPT_OFFLINE="$(mktemp -d)/fv-offline-$(date +%s).md"

(
  cd "${REPO_ROOT}"
  GITCODE_LIVE_TEST="" \
  GITCODE_TOKEN="" \
  "${VSCRIPT}" \
    --fixtures testdata/fixtures \
    --transcript "${TRANSCRIPT_OFFLINE}"
)
VSCRIPT_RC=$?

if [[ ! -f "${TRANSCRIPT_OFFLINE}" ]]; then
  fail "scenario-1: no transcript produced"
else
  pass "scenario-1: transcript produced at ${TRANSCRIPT_OFFLINE}"
fi

if [[ "${VSCRIPT_RC}" -ne 0 ]]; then
  fail "scenario-1: validate-fixtures.sh exited ${VSCRIPT_RC}, expected 0"
else
  pass "scenario-1: exit code 0"
fi

# ---- scenario 3: transcript content and safety ----

echo ""
echo "--- Scenario 3: transcript content and safety ---"

if ! grep -q 'offline_pass' "${TRANSCRIPT_OFFLINE}" 2>/dev/null; then
  fail "scenario-3: transcript missing offline_pass classification"
else
  pass "scenario-3: offline_pass classification present"
fi

LIVE_SKIP_CLASS=$(grep -oE 'live_skipped_(no_flag|no_token)' "${TRANSCRIPT_OFFLINE}" 2>/dev/null || true)
if [[ -z "${LIVE_SKIP_CLASS}" ]]; then
  fail "scenario-3: transcript missing live_skipped_no_flag or live_skipped_no_token"
else
  pass "scenario-3: live skipped classification found: ${LIVE_SKIP_CLASS}"
fi

source "${SAFETY_LIB}"
if ! assert_public_safe_transcript "${TRANSCRIPT_OFFLINE}"; then
  fail "scenario-3: transcript failed public-safety check"
else
  pass "scenario-3: transcript passes public-safety check"
fi

# ---- scenario 4: live validation with mock token (redaction test) ----

echo ""
echo "--- Scenario 4: live validation with mock token ---"

TRANSCRIPT_LIVE="$(mktemp -d)/fv-live-$(date +%s).md"
MOCK_TOKEN="ghp_testTokenValueThatMustBeRedactedFromOutput"

(
  cd "${REPO_ROOT}"
  GITCODE_LIVE_TEST=1 \
  GITCODE_TOKEN="${MOCK_TOKEN}" \
  "${VSCRIPT}" \
    --fixtures testdata/fixtures \
    --transcript "${TRANSCRIPT_LIVE}" \
    --live
) || true

if [[ ! -f "${TRANSCRIPT_LIVE}" ]]; then
  fail "scenario-4: no transcript produced for live run"
else
  pass "scenario-4: live transcript produced"

  LIVE_OUTCOME=$(grep -oE 'live_(pass_redacted|fail_redacted)' "${TRANSCRIPT_LIVE}" 2>/dev/null || true)
  if [[ -z "${LIVE_OUTCOME}" ]]; then
    fail "scenario-4: transcript missing live_pass_redacted or live_fail_redacted"
  else
    pass "scenario-4: live outcome classification: ${LIVE_OUTCOME}"
  fi

  if grep -qF "${MOCK_TOKEN}" "${TRANSCRIPT_LIVE}" 2>/dev/null; then
    fail "scenario-4: raw token value found in transcript — REDACTION FAILURE"
  else
    pass "scenario-4: raw token not found in transcript"
  fi

  if ! assert_public_safe_transcript "${TRANSCRIPT_LIVE}"; then
    fail "scenario-4: live transcript failed public-safety check"
  else
    pass "scenario-4: live transcript passes public-safety check"
  fi

  if grep -qF 'GITCODE_TOKEN: `<REDACTED_TOKEN>`' "${TRANSCRIPT_LIVE}" 2>/dev/null; then
    pass "scenario-4: token header correctly redacted"
  else
    fail "scenario-4: token header not redacted as expected"
  fi
fi

# ---- scenario 2: docs-smoke.commands manifest entry ----

echo ""
echo "--- Scenario 2: docs-smoke.commands manifest entry ---"

SMOKE_LINE=$(grep -n 'fixture-validation-gate' "${MANIFEST}" 2>/dev/null || true)
if [[ -z "${SMOKE_LINE}" ]]; then
  fail "scenario-2: fixture-validation-gate entry not found in docs-smoke.commands"
else
  LINE_NUM=$(echo "${SMOKE_LINE}" | cut -d: -f1)
  if echo "${SMOKE_LINE}" | grep -q 'success'; then
    pass "scenario-2: fixture-validation-gate at line ${LINE_NUM} includes 'success' in allowed_outcomes"
  else
    fail "scenario-2: fixture-validation-gate missing 'success' in allowed_outcomes (got: ${SMOKE_LINE})"
  fi
fi

# Scenario 1 re-verify: live_skipped_no_token when GITCODE_LIVE_TEST=1 but no --live
echo ""
echo "--- Scenario 3b: live_skipped_no_token when GITCODE_LIVE_TEST=1 but no --live ---"

TRANSCRIPT_LIVE_ENV_NO_LIVE="$(mktemp -d)/fv-live-env-no-live-$(date +%s).md"
(
  cd "${REPO_ROOT}"
  GITCODE_LIVE_TEST=1 \
  GITCODE_TOKEN="" \
  "${VSCRIPT}" \
    --fixtures testdata/fixtures \
    --transcript "${TRANSCRIPT_LIVE_ENV_NO_LIVE}"
)
if grep -q 'live_skipped_no_flag' "${TRANSCRIPT_LIVE_ENV_NO_LIVE}" 2>/dev/null; then
  pass "scenario-3b: GITCODE_LIVE_TEST=1 without --live → live_skipped_no_flag"
else
  fail "scenario-3b: expected live_skipped_no_flag when GITCODE_LIVE_TEST=1 but no --live"
fi

# ---- summary ----

echo ""
echo "========================================"
if [[ "${FAILURES}" -eq 0 ]]; then
  green "ALL VALIDATION CHECKS PASSED"
  exit 0
else
  red "${FAILURES} VALIDATION CHECK(S) FAILED"
  exit 1
fi
