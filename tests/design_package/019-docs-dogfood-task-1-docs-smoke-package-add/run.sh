#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

TMPDIR="${TMPDIR:-/tmp}"
TMP_ROOT="$(mktemp -d "${TMPDIR}/019-validation.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

CACHE_PATH="${TMP_ROOT}/gitcode-mcp.db"
TRANSCRIPT="${TMP_ROOT}/docs-smoke.md"
FIXTURE_CONFIG="${REPO_ROOT}/testdata/configs/dogfood.yaml"
SMOKE_SCRIPT="${REPO_ROOT}/project/dogfood/docs-smoke.sh"
SAFETY_LIB="${REPO_ROOT}/project/dogfood/lib/safety.sh"
MANIFEST="${REPO_ROOT}/project/dogfood/docs-smoke.commands"
ALLOWLIST="${REPO_ROOT}/project/dogfood/fixture-allowlist.txt"

PASSED=0
FAILED=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  FAILED=$((FAILED + 1))
}

pass() {
  printf 'PASS: %s\n' "$1"
  PASSED=$((PASSED + 1))
}

# ────────────────────────────────────────────────────────────
# Scenario 1: Smokerunner exits 0 and produces redacted transcript
# ────────────────────────────────────────────────────────────
printf '\n=== Scenario 1: Docs smoke runner produces redacted transcript ===\n'

# Kill any previously running servers on port 19020
lsof -ti:19020 2>/dev/null | xargs kill -9 2>/dev/null || true
sleep 0.5

printf 'Running docs-smoke.sh with fixture config...\n'
rm -f "${CACHE_PATH}" "${CACHE_PATH}.lock" "${CACHE_PATH}-wal" "${CACHE_PATH}-shm"

if GITCODE_MCP_CONFIG="${FIXTURE_CONFIG}" \
   bash "${SMOKE_SCRIPT}" \
     --fixture-config "${FIXTURE_CONFIG}" \
     --cache-path "${CACHE_PATH}" \
     --transcript "${TRANSCRIPT}" >/dev/null 2>&1; then
  pass "docs-smoke.sh exited 0"
else
  fail "docs-smoke.sh exited non-zero"
fi

if [[ -f "${TRANSCRIPT}" && -s "${TRANSCRIPT}" ]]; then
  pass "redacted transcript exists and is non-empty"
else
  fail "redacted transcript is missing or empty"
fi

# Count undocumented_failure occurrences
UNDOC_COUNT="$(grep -c 'undocumented_failure' "${TRANSCRIPT}" 2>/dev/null || true)"
if [[ "${UNDOC_COUNT}" -eq 0 ]]; then
  pass "no undocumented_failure outcomes in transcript"
else
  fail "${UNDOC_COUNT} undocumented_failure outcome(s) found in transcript"
fi

# ────────────────────────────────────────────────────────────
# Scenario 2: Command manifest covers documented workflow surfaces
# ────────────────────────────────────────────────────────────
printf '\n=== Scenario 2: Command manifest covers documented workflow ===\n'

if [[ ! -f "${MANIFEST}" ]]; then
  fail "command manifest missing: ${MANIFEST}"
else
  DOC_SURFACES=(
    "docs/install.md"
    "docs/config-reference.md"
    "docs/secrets.md"
    "docs/repo-binding.md"
    "docs/read-walkthrough.md"
    "docs/mcp-setup.md"
    "docs/write-walkthrough.md"
    "docs/fixture-capture.md"
  )

  for doc in "${DOC_SURFACES[@]}"; do
    if grep -q "|${doc}|" "${MANIFEST}" 2>/dev/null; then
      pass "manifest references ${doc}"
    else
      fail "manifest does not reference ${doc}"
    fi
  done
fi

# Verify each manifest entry has all 7 pipe-delimited fields
MANIFEST_MALFORMED=0
while IFS= read -r line || [[ -n "${line}" ]]; do
  [[ -z "${line}" || "${line}" == \#* ]] && continue
  FIELDS="$(echo "${line}" | tr '|' '\n' | wc -l | tr -d ' ')"
  if [[ "${FIELDS}" -ne 7 ]]; then
    MANIFEST_MALFORMED=$((MANIFEST_MALFORMED + 1))
  fi
done < "${MANIFEST}"
if [[ ${MANIFEST_MALFORMED} -eq 0 ]]; then
  pass "all manifest entries have 7 pipe-delimited fields"
else
  fail "${MANIFEST_MALFORMED} manifest entries have incorrect field count"
fi

# ────────────────────────────────────────────────────────────
# Scenario 3: Redacted transcript safety and MCP output
# ────────────────────────────────────────────────────────────
printf '\n=== Scenario 3: Redacted transcript safety and MCP output ===\n'

# Safety check
if [[ -f "${SAFETY_LIB}" ]]; then
  # shellcheck source=/dev/null
  source "${SAFETY_LIB}"
  load_fixture_allowlist
  if assert_public_safe_transcript "${TRANSCRIPT}" 2>/dev/null; then
    pass "redacted transcript passes public-safety check"
  else
    fail "redacted transcript failed public-safety check"
  fi
else
  fail "safety.sh not found at ${SAFETY_LIB}"
fi

# MCP step output contains fixture content
if grep -q 'remote issue body' "${TRANSCRIPT}" 2>/dev/null; then
  pass "MCP step transcript contains fixture snippet content"
else
  fail "MCP step transcript does not contain expected fixture content"
fi

# ────────────────────────────────────────────────────────────
# Scenario 4: Executable evidence (--help, bash syntax, go test)
# ────────────────────────────────────────────────────────────
printf '\n=== Scenario 4: Executable evidence ===\n'

if bash -n "${SMOKE_SCRIPT}" 2>/dev/null; then
  pass "docs-smoke.sh passes bash syntax check"
else
  fail "docs-smoke.sh has bash syntax errors"
fi

if bash "${SMOKE_SCRIPT}" --help >/dev/null 2>&1; then
  pass "docs-smoke.sh --help exits 0"
else
  fail "docs-smoke.sh --help exits non-zero"
fi

if bash -n "${SAFETY_LIB}" 2>/dev/null; then
  pass "safety.sh passes bash syntax check"
else
  fail "safety.sh has bash syntax errors"
fi

# Go tests
printf 'Running go test ./...\n'
if (cd "${REPO_ROOT}" && go test ./... >/dev/null 2>&1); then
  pass "go test ./... passes"
else
  fail "go test ./... failed"
fi

# ────────────────────────────────────────────────────────────
# Additional product surface validations
# ────────────────────────────────────────────────────────────
printf '\n=== Additional product surface validation ===\n'

# Verify all 9 docs files exist and are non-empty
DOCS=(
  "docs/install.md"
  "docs/config-reference.md"
  "docs/repo-binding.md"
  "docs/secrets.md"
  "docs/mcp-setup.md"
  "docs/read-walkthrough.md"
  "docs/write-walkthrough.md"
  "docs/troubleshooting.md"
  "docs/fixture-capture.md"
)
for doc in "${DOCS[@]}"; do
  if [[ -f "${REPO_ROOT}/${doc}" && -s "${REPO_ROOT}/${doc}" ]]; then
    pass "${doc} exists and is non-empty"
  else
    fail "${doc} is missing or empty"
  fi
done

# Verify safety.sh has all four required functions
SAFETY_FUNCS=("load_fixture_allowlist" "redact_transcript" "assert_public_safe_transcript" "write_redacted_transcript")
for func in "${SAFETY_FUNCS[@]}"; do
  if grep -q "^${func}()" "${SAFETY_LIB}" 2>/dev/null; then
    pass "safety.sh contains ${func}"
  else
    fail "safety.sh missing function ${func}"
  fi
done

# Verify fixture allowlist has required sections
for section in "fixture_ids" "fixture_hosts" "placeholders"; do
  if grep -q "\[${section}\]" "${ALLOWLIST}" 2>/dev/null; then
    pass "fixture-allowlist.txt has [${section}] section"
  else
    fail "fixture-allowlist.txt missing [${section}] section"
  fi
done

# Verify dogfood.yaml is valid YAML syntax (basic check: has key: value pairs)
if grep -q 'gitcode_base_url:' "${FIXTURE_CONFIG}" && \
   grep -q 'credential:' "${FIXTURE_CONFIG}"; then
  pass "dogfood.yaml appears valid"
else
  fail "dogfood.yaml appears malformed"
fi

# Verify git diff --check is clean
if (cd "${REPO_ROOT}" && git diff --check >/dev/null 2>&1); then
  pass "git diff --check passes"
else
  fail "git diff --check found whitespace violations"
fi

# ────────────────────────────────────────────────────────────
# Summary
# ────────────────────────────────────────────────────────────
printf '\n=== Summary ===\n'
printf 'Passed: %d\n' "${PASSED}"
printf 'Failed: %d\n' "${FAILED}"
printf '\n'

if [[ ${FAILED} -gt 0 ]]; then
  exit 1
fi
exit 0
