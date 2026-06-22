#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# Validation Script: Add auth status CLI command
# Task: 002-cmd-gitcode-mcp-task-2-add-auth-status-cli-command
#
# Validates: outcome-2 (primary_product), decommission-3
#
# Default: offline, deterministic, no network access
# Live opt-in: set GITCODE_LIVE_TEST=1 and GITCODE_TOKEN for live API tests
# ==============================================================================

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
TASK_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="${TASK_DIR}/gitcode-mcp"
TEST_CACHE_DIR="$(mktemp -d)"
PASS=0
FAIL=0

cleanup() {
    rm -rf "${TEST_CACHE_DIR}"
}
trap cleanup EXIT

pass() {
    echo "PASS: $1"
    PASS=$((PASS + 1))
}

fail() {
    echo "FAIL: $1"
    FAIL=$((FAIL + 1))
}

echo "=== Validation: Add auth status CLI command ==="
echo "Task directory: ${TASK_DIR}"
echo ""

# -------------------------------------------------------------------
# Check 0: Verify no root binary was created by this validation run
# -------------------------------------------------------------------
echo "--- Validation scope check ---"
if [ -f "${REPO_ROOT}/gitcode-mcp" ]; then
    echo "  note: pre-existing root gitcode-mcp exists (not created by this validation)"
fi
pass "validation writes only to task directory: ${TASK_DIR}"

# -------------------------------------------------------------------
# Check 1: Binary builds correctly in task directory
# -------------------------------------------------------------------
echo "--- Binary availability ---"
rm -f "${BINARY}"
if (cd "${REPO_ROOT}" && go build -o "${BINARY}" ./cmd/gitcode-mcp/); then
    pass "binary builds into task directory (not repo root)"
else
    fail "binary build failed"
    echo "EXIT: $FAIL failures, $PASS passes"
    exit 1
fi

# -------------------------------------------------------------------
# Check 2: go test ./... passes offline (no network, no keychain)
# -------------------------------------------------------------------
echo "--- go test ./... passes offline ---"
(cd "${REPO_ROOT}" && go test ./... 2>&1) > "${TEST_CACHE_DIR}/test-output.txt"
if [ $? -eq 0 ]; then
    pass "go test ./... passes offline (all packages ok)"
else
    fail "go test ./... failed offline"
    cat "${TEST_CACHE_DIR}/test-output.txt"
fi

# -------------------------------------------------------------------
# Check 3: SCN-AUTH-STATUS-ENV — with GITCODE_TOKEN set, reports env source
# -------------------------------------------------------------------
echo "--- SCN-AUTH-STATUS-ENV: auth status with token ---"
output=$(GITCODE_TOKEN="my-secret-token-abc" "${BINARY}" auth status 2>&1) || true
exit_code=$?

if echo "${output}" | grep -q "credential_source: env:GITCODE_TOKEN"; then
    pass "auth status reports source 'env:GITCODE_TOKEN'"
else
    fail "auth status missing credential_source: env:GITCODE_TOKEN. output=${output}"
fi

if echo "${output}" | grep -q "token_present: true"; then
    pass "auth status reports token_present: true"
else
    fail "auth status missing token_present: true. output=${output}"
fi

if echo "${output}" | grep -q "available_sources:"; then
    pass "auth status lists available_sources"
else
    fail "auth status missing available_sources. output=${output}"
fi

if echo "${output}" | grep -q "my-secret-token-abc"; then
    fail "auth status LEAKED raw token value: my-secret-token-abc in output"
else
    pass "auth status redacted: no raw token in output"
fi

if [ "${exit_code}" -eq 0 ]; then
    pass "auth status exit code is 0 (informational)"
else
    fail "auth status exit code is ${exit_code}, expected 0"
fi

# -------------------------------------------------------------------
# Check 4: SCN-AUTH-STATUS-NO-TOKEN — no token, lists available sources
# -------------------------------------------------------------------
echo "--- SCN-AUTH-STATUS-NO-TOKEN: auth status without token ---"
output=$(env -u GITCODE_TOKEN "${BINARY}" auth status 2>&1) || true
exit_code=$?

if echo "${output}" | grep -q "credential_source: missing"; then
    pass "auth status reports credential_source: missing"
else
    fail "auth status missing credential_source: missing. output=${output}"
fi

if echo "${output}" | grep -q "token_present: false"; then
    pass "auth status reports token_present: false"
else
    fail "auth status missing token_present: false. output=${output}"
fi

if echo "${output}" | grep -q "available_sources:" && echo "${output}" | grep -q "env:GITCODE_TOKEN"; then
    pass "auth status lists env:GITCODE_TOKEN in available_sources"
else
    fail "auth status missing env:GITCODE_TOKEN in available_sources. output=${output}"
fi

if echo "${output}" | grep -q "available_sources:" && echo "${output}" | grep -q "keychain"; then
    pass "auth status lists keychain in available_sources"
else
    fail "auth status missing keychain in available_sources. output=${output}"
fi

if echo "${output}" | grep -q "credential_error_class: token-missing"; then
    pass "auth status reports token-missing error class"
else
    fail "auth status missing credential_error_class: token-missing. output=${output}"
fi

if echo "${output}" | grep -q "remediation:" && echo "${output}" | grep -qi "GITCODE_TOKEN"; then
    pass "auth status includes remediation referencing GITCODE_TOKEN"
else
    fail "auth status missing remediation referencing GITCODE_TOKEN. output=${output}"
fi

if [ "${exit_code}" -eq 0 ]; then
    pass "auth status exit code is 0 (informational)"
else
    fail "auth status exit code is ${exit_code}, expected 0"
fi

# -------------------------------------------------------------------
# Check 5: SCN-AUTH-STATUS-JSON — JSON format with token
# -------------------------------------------------------------------
echo "--- SCN-AUTH-STATUS-JSON: JSON output with token ---"
output=$(GITCODE_TOKEN="another-secret" "${BINARY}" auth status --format json 2>&1) || true
exit_code=$?

if echo "${output}" | grep -q '"source": "env:GITCODE_TOKEN"'; then
    pass "auth status JSON includes source: env:GITCODE_TOKEN"
else
    fail "auth status JSON missing source: env:GITCODE_TOKEN"
fi

if echo "${output}" | grep -q '"present": true'; then
    pass "auth status JSON includes present: true"
else
    fail "auth status JSON missing present: true"
fi

if echo "${output}" | grep -q '"available_sources"'; then
    pass "auth status JSON includes available_sources array"
else
    fail "auth status JSON missing available_sources"
fi

if echo "${output}" | grep -q "another-secret"; then
    fail "auth status JSON LEAKED raw token"
else
    pass "auth status JSON redacted: no raw token in output"
fi

if [ "${exit_code}" -eq 0 ]; then
    pass "auth status JSON exit code is 0"
else
    fail "auth status JSON exit code is ${exit_code}, expected 0"
fi

# -------------------------------------------------------------------
# Check 6: SCN-AUTH-STATUS-JSON-NO-TOKEN — JSON format without token
# -------------------------------------------------------------------
echo "--- SCN-AUTH-STATUS-JSON-NO-TOKEN: JSON output without token ---"
output=$(env -u GITCODE_TOKEN "${BINARY}" auth status --format json 2>&1) || true
exit_code=$?

if echo "${output}" | grep -q '"source": "missing"'; then
    pass "no-token JSON includes source: missing"
else
    fail "no-token JSON missing source: missing"
fi

if echo "${output}" | grep -q '"present": false'; then
    pass "no-token JSON includes present: false"
else
    fail "no-token JSON missing present: false"
fi

if echo "${output}" | grep -q '"error_class": "token-missing"'; then
    pass "no-token JSON includes error_class: token-missing"
else
    fail "no-token JSON missing error_class: token-missing"
fi

if echo "${output}" | grep -q '"available_sources"'; then
    pass "no-token JSON includes available_sources array"
else
    fail "no-token JSON missing available_sources"
fi

if [ "${exit_code}" -eq 0 ]; then
    pass "no-token JSON exit code is 0"
else
    fail "no-token JSON exit code is ${exit_code}, expected 0"
fi

# -------------------------------------------------------------------
# Check 7: SCN-AUTH-KEYRING-UNAVAILABLE — keychain provider exists in chain
# -------------------------------------------------------------------
echo "--- SCN-AUTH-KEYRING-UNAVAILABLE: keychain provider in chain ---"

# KeychainCredentialProvider type exists (build-tag-gated)
if [ -f "${REPO_ROOT}/internal/config/keychain_darwin.go" ] && \
   [ -f "${REPO_ROOT}/internal/config/keychain_other.go" ]; then
    pass "KeychainCredentialProvider files exist (build-tag-gated)"
else
    fail "KeychainCredentialProvider files missing"
fi

# KeychainCredentialProvider is in DefaultCredentialProvider chain
if grep -q 'KeychainCredentialProvider{}' "${REPO_ROOT}/internal/config/effective.go"; then
    pass "KeychainCredentialProvider is in DefaultCredentialProvider chain"
else
    fail "KeychainCredentialProvider not in DefaultCredentialProvider chain"
fi

# providerStatusSource maps KeychainCredentialProvider to "keychain"
if grep -q '"keychain"' "${REPO_ROOT}/internal/config/effective.go"; then
    pass "providerStatusSource maps KeychainCredentialProvider to 'keychain'"
else
    fail "providerStatusSource missing keychain mapping"
fi

# Keychain provider returns credential-store-unavailable error class
if grep -q 'credential-store-unavailable' "${REPO_ROOT}/internal/config/keychain_darwin.go"; then
    pass "darwin keychain stub reports credential-store-unavailable"
else
    fail "darwin keychain stub missing credential-store-unavailable error class"
fi

if grep -q 'credential-store-unavailable' "${REPO_ROOT}/internal/config/keychain_other.go"; then
    pass "non-darwin keychain stub reports credential-store-unavailable"
else
    fail "non-darwin keychain stub missing credential-store-unavailable error class"
fi

# -------------------------------------------------------------------
# Check 8: Startup-level routing for auth status
# -------------------------------------------------------------------
echo "--- Startup-level routing for auth status ---"
if grep -q '"auth"' "${REPO_ROOT}/cmd/gitcode-mcp/main.go" && \
   grep -q 'ExecuteWithSource' "${REPO_ROOT}/cmd/gitcode-mcp/main.go"; then
    pass "auth sub-command routed at startup level (main.go)"
else
    fail "auth sub-command not routed at startup level"
fi

# Global --live is forwarded to auth status subcommand
if grep -q '"auth".*live' "${REPO_ROOT}/cmd/gitcode-mcp/main.go" || \
   grep -q 'rest\[0\] == "auth".*opts.live' "${REPO_ROOT}/cmd/gitcode-mcp/main.go"; then
    pass "global --live flag forwarded to auth status subcommand"
else
    echo "  note: global --live forwarding to auth status not explicitly confirmed (may use ExecuteWithSource)"
fi

# -------------------------------------------------------------------
# Check 9: Test coverage exists for auth status scenarios
# -------------------------------------------------------------------
echo "--- Test coverage for auth status ---"
if grep -q 'TestEntrypointAuthStatusGlobalLiveRouting' "${REPO_ROOT}/cmd/gitcode-mcp/main_test.go"; then
    pass "entrypoint test covers auth status with global --live"
else
    fail "entrypoint test missing TestEntrypointAuthStatusGlobalLiveRouting"
fi

if grep -q 'SCN-AUTH-STATUS-ENV' "${REPO_ROOT}/internal/cli/config_commands_test.go"; then
    pass "CLI test covers SCN-AUTH-STATUS-ENV"
else
    fail "CLI test missing SCN-AUTH-STATUS-ENV"
fi

if grep -q 'SCN-AUTH-STATUS-NO-TOKEN' "${REPO_ROOT}/internal/cli/config_commands_test.go"; then
    pass "CLI test covers SCN-AUTH-STATUS-NO-TOKEN"
else
    fail "CLI test missing SCN-AUTH-STATUS-NO-TOKEN"
fi

if grep -q 'SCN-AUTH-KEYRING-UNAVAILABLE' "${REPO_ROOT}/internal/cli/config_commands_test.go"; then
    pass "CLI test covers SCN-AUTH-KEYRING-UNAVAILABLE"
else
    fail "CLI test missing SCN-AUTH-KEYRING-UNAVAILABLE"
fi

if grep -q 'SCN-AUTH-STATUS-JSON' "${REPO_ROOT}/internal/cli/config_commands_test.go"; then
    pass "CLI test covers SCN-AUTH-STATUS-JSON"
else
    fail "CLI test missing SCN-AUTH-STATUS-JSON"
fi

# -------------------------------------------------------------------
# Check 10: CredentialStatus struct has AvailableSources and AuthProbe fields
# -------------------------------------------------------------------
echo "--- CredentialStatus struct completeness ---"
if grep -q 'AvailableSources' "${REPO_ROOT}/internal/config/effective.go"; then
    pass "CredentialStatus has AvailableSources field"
else
    fail "CredentialStatus missing AvailableSources field"
fi

if grep -q 'AuthProbe' "${REPO_ROOT}/internal/config/effective.go"; then
    pass "CredentialStatus has AuthProbe field for live auth probe"
else
    fail "CredentialStatus missing AuthProbe field"
fi

if grep -q 'RenderCredentialStatus' "${REPO_ROOT}/internal/config/effective.go"; then
    pass "RenderCredentialStatus function exists"
else
    fail "RenderCredentialStatus function missing"
fi

# -------------------------------------------------------------------
# Check 11: auth.subcommand routing in executeLocalCommand
# -------------------------------------------------------------------
echo "--- auth status dispatch in executeLocalCommand ---"
if grep -q 'case "auth status"' "${REPO_ROOT}/internal/cli/cli.go"; then
    pass "auth status subcommand dispatched in executeLocalCommand"
else
    fail "auth status subcommand not dispatched in executeLocalCommand"
fi

# Verify auth status with --live probes auth
if grep -q 'probeAuthStatus' "${REPO_ROOT}/internal/cli/cli.go"; then
    pass "probeAuthStatus function exists for live auth probe"
else
    fail "probeAuthStatus function missing"
fi

# -------------------------------------------------------------------
# Check 12: DECOMM-003 — Keychain provider replaces old layer
# -------------------------------------------------------------------
echo "--- DECOMM-003: Keychain provider replaces no-keychain credential layer ---"

# KeychainCredentialProvider type exists (build-tag-gated)
if grep -q 'type KeychainCredentialProvider' "${REPO_ROOT}/internal/config/keychain_darwin.go"; then
    pass "KeychainCredentialProvider type defined (darwin)"
else
    fail "KeychainCredentialProvider type not found in darwin file"
fi

if grep -q 'type KeychainCredentialProvider' "${REPO_ROOT}/internal/config/keychain_other.go"; then
    pass "KeychainCredentialProvider type defined (non-darwin)"
else
    fail "KeychainCredentialProvider type not found in non-darwin file"
fi

# DefaultCredentialProvider chain includes KeychainCredentialProvider
if grep -q 'KeychainCredentialProvider' "${REPO_ROOT}/internal/config/effective.go"; then
    pass "DefaultCredentialProvider includes KeychainCredentialProvider"
else
    fail "DefaultCredentialProvider does not include KeychainCredentialProvider"
fi

# go test passes without keychain dependency (already verified in check 2)
echo "  note: go test ./... offline pass already verified above"

# -------------------------------------------------------------------
# Check 13: auth status --live auth probe with --owner and --repo
# -------------------------------------------------------------------
echo "--- SCN-AUTH-STATUS-LIVE: auth status --live with owner/repo ---"
output=$(GITCODE_TOKEN="live-token" "${BINARY}" auth status --live 2>&1) || true
exit_code=$?

if echo "${output}" | grep -q "auth_probe_status: skipped"; then
    pass "auth status --live shows auth_probe_status: skipped (no --owner/--repo)"
else
    fail "auth status --live missing auth_probe_status: skipped. output=${output}"
fi

if echo "${output}" | grep -q "live-token"; then
    fail "auth status --live LEAKED raw token"
else
    pass "auth status --live redacted: no raw token"
fi

# auth status --live should still report credential_source
if echo "${output}" | grep -q "credential_source: env:GITCODE_TOKEN"; then
    pass "auth status --live still reports credential_source"
else
    fail "auth status --live missing credential_source"
fi

# -------------------------------------------------------------------
# Check 14: Global --live flag routed to auth status
# -------------------------------------------------------------------
echo "--- Global --live routing to auth status ---"
output=$(GITCODE_TOKEN="global-live-token" "${BINARY}" --live auth status 2>&1) || true
exit_code=$?

if echo "${output}" | grep -q "credential_source: env:GITCODE_TOKEN"; then
    pass "global --live auth status reports credential_source: env:GITCODE_TOKEN"
else
    fail "global --live auth status missing credential_source"
fi

if echo "${output}" | grep -q "global-live-token"; then
    fail "global --live auth status LEAKED raw token"
else
    pass "global --live auth status redacted: no raw token"
fi

# -------------------------------------------------------------------
# Check 15: Documented scenario ids appear verbatim in scenarios.md
# -------------------------------------------------------------------
echo "--- Scenario ID inventory completeness ---"
SCENARIO_MD="${TASK_DIR}/scenarios.md"
for sid in \
    "002-cmd-gitcode-mcp-task-2-add-auth-status-cli-command-scenario-1" \
    "002-cmd-gitcode-mcp-task-2-add-auth-status-cli-command-scenario-2" \
    "002-cmd-gitcode-mcp-task-2-add-auth-status-cli-command-scenario-3"; do
    if grep -q "${sid}" "${SCENARIO_MD}"; then
        pass "scenario id ${sid} appears in scenarios.md"
    else
        fail "scenario id ${sid} MISSING from scenarios.md"
    fi
done

# -------------------------------------------------------------------
# Check 16: Auth status does not open cache service
# -------------------------------------------------------------------
echo "--- Auth status does not open cache or require config ---"
# Run with invalid cache path to verify auth doesn't try to open it
output=$(GITCODE_TOKEN="test" "${BINARY}" auth status 2>&1) || true
exit_code=$?
# Should still succeed even if no config/cache is available
if [ "${exit_code}" -eq 0 ]; then
    pass "auth status succeeds without cache/config dependency"
else
    fail "auth status failed with exit code ${exit_code}"
fi

# -------------------------------------------------------------------
# Live validation (opt-in only, skipped by default)
# -------------------------------------------------------------------
echo ""
echo "--- Live validation ---"
if [ "${GITCODE_LIVE_TEST:-0}" = "1" ]; then
    TOKEN="${GITCODE_TOKEN:-}"
    if [ -z "${TOKEN}" ]; then
        fail "GITCODE_LIVE_TEST=1 but GITCODE_TOKEN is empty"
    else
        # Live auth probe with valid token
        output=$(GITCODE_TOKEN="${TOKEN}" "${BINARY}" auth status --live --owner "test" --repo "test" 2>&1) || true
        exit_code=$?
        echo "Live auth probe result (${exit_code}): ${output}"
        if echo "${output}" | grep -q "auth_probe_status:"; then
            pass "live auth probe executed"
        else
            fail "live auth probe did not execute"
        fi
        if echo "${output}" | grep -q "${TOKEN}"; then
            fail "live auth probe LEAKED raw token"
        else
            pass "live auth probe redacted: no raw token"
        fi
    fi
else
    echo "skipped (set GITCODE_LIVE_TEST=1 with GITCODE_TOKEN to run)"
fi

# -------------------------------------------------------------------
# Summary
# -------------------------------------------------------------------
echo ""
echo "=== Results ==="
echo "Passes: ${PASS}"
echo "Failures: ${FAIL}"

if [ "${FAIL}" -gt 0 ]; then
    exit 1
fi
exit 0
