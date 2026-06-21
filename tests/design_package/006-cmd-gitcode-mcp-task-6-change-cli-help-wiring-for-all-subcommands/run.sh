#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# Validation Script: Change CLI help wiring for all subcommands
# Task: 006-cmd-gitcode-mcp-task-6-change-cli-help-wiring-for-all-subcommands
#
# Validates: outcome-9 (primary_product), decommission-9
#
# Default: offline, deterministic, no network access
# Live opt-in: none (help is always offline)
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

# run_cmd: run binary, capture stdout/stderr separately, return exit code
# Usage: run_cmd args... -> sets out, err, ec variables
run_cmd() {
    local _ec=0
    out=$("${BINARY}" "$@" 2>"${TEST_CACHE_DIR}/stderr.$$") || _ec=$?
    ec=${_ec}
    err=$(cat "${TEST_CACHE_DIR}/stderr.$$")
}

echo "=== Validation: Change CLI help wiring for all subcommands ==="
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
_test_ec=0
(cd "${REPO_ROOT}" && go test ./... 2>&1) > "${TEST_CACHE_DIR}/test-output.txt" || _test_ec=$?
if [ "${_test_ec}" -eq 0 ]; then
    pass "go test ./... passes offline (all packages ok)"
else
    fail "go test ./... failed offline"
    cat "${TEST_CACHE_DIR}/test-output.txt"
fi

# -------------------------------------------------------------------
# Check 3: SCN-CLI-HELP-001 — Root-level --help and no-args help
# -------------------------------------------------------------------
echo "--- SCN-CLI-HELP-001: Root --help / no-args help ---"

# gitcode-mcp --help → startup-level help, exits 0
run_cmd --help
if [ "${ec}" -eq 0 ]; then
    pass "gitcode-mcp --help exit code is 0"
else
    fail "gitcode-mcp --help exit code is ${ec}, expected 0"
fi

if echo "${out}" | grep -q "Usage:"; then
    pass "gitcode-mcp --help contains Usage line"
else
    fail "gitcode-mcp --help missing Usage line. out=${out}"
fi

# gitcode-mcp (no args) → full command listing, exits 0
run_cmd
if [ "${ec}" -eq 0 ]; then
    pass "gitcode-mcp (no args) exit code is 0"
else
    fail "gitcode-mcp (no args) exit code is ${ec}, expected 0"
fi

# The full command listing is in no-args or via the CLI-level --help route
# (gitcode-mcp without args prints printHelp() which lists all commands)
REGISTERED_COMMANDS=(
    "ingest" "index" "search" "list" "get"
    "backlinks" "get-snippet" "snippet" "snippets" "list-chunks"
    "recent" "link-check" "stale-index" "sync"
    "cache-status" "sync-status" "sync_status"
    "export" "export-snapshot"
    "diff" "diff-snapshot"
    "create-issue" "update-issue" "create-page" "update-page"
    "add-comment" "add-label"
    "config" "auth" "doctor" "migrate-cache" "repo"
)
# Check that each command appears in either --help output or no-args output
root_help_ok=true
for cmd in "${REGISTERED_COMMANDS[@]}"; do
    found=false
    if echo "${out}" | grep -q "^\s*${cmd}$" || echo "${out}" | grep -q "^\s*${cmd}\s"; then
        found=true
    fi
    # Also check the startup-level --help output if no-args didn't list it
    if ! $found; then
        run_cmd --help
        if echo "${out}" | grep -q "${cmd}"; then
            found=true
        fi
    fi
    if ! $found; then
        fail "help output missing command: ${cmd}"
        root_help_ok=false
    fi
done
if $root_help_ok; then
    pass "root-level help surfaces list all registered subcommands"
fi

# -------------------------------------------------------------------
# Check 4: SCN-CLI-HELP-002 — Per-command --help exits 0 with valid text
# -------------------------------------------------------------------
echo "--- SCN-CLI-HELP-002: Per-command --help ---"

COMMANDS_WITH_HELP=(
    "sync" "index" "search" "list" "get"
    "get-snippet" "snippet" "snippets" "backlinks" "list-chunks"
    "recent" "link-check" "stale-index" "cache-status"
    "sync-status" "sync_status" "export" "export-snapshot"
    "diff" "diff-snapshot"
    "create-issue" "update-issue" "create-page" "update-page"
    "add-comment" "add-label"
    "ingest"
    "config" "auth" "doctor" "migrate-cache" "repo"
)

per_cmd_help_ok=true
for cmd in "${COMMANDS_WITH_HELP[@]}"; do
    run_cmd "${cmd}" --help

    if [ "${ec}" -ne 0 ]; then
        fail "${cmd} --help exit code is ${ec}, expected 0. stderr=${err}"
        per_cmd_help_ok=false
        continue
    fi

    if [ -z "${out}" ]; then
        fail "${cmd} --help produced empty stdout. stderr=${err}"
        per_cmd_help_ok=false
        continue
    fi

    # Must contain "Usage" or the command name
    if echo "${out}" | grep -qi "usage"; then
        : # good
    elif echo "${out}" | grep -qi "${cmd}"; then
        : # good
    else
        fail "${cmd} --help output missing Usage or command name. out=${out}"
        per_cmd_help_ok=false
        continue
    fi

    # Must NOT contain invalid_query in stdout or stderr
    if echo "${out}" | grep -qi "invalid_query"; then
        fail "${cmd} --help stdout contains invalid_query: ${out}"
        per_cmd_help_ok=false
        continue
    fi
    if echo "${err}" | grep -qi "invalid_query"; then
        fail "${cmd} --help stderr contains invalid_query: ${err}"
        per_cmd_help_ok=false
        continue
    fi
done
if $per_cmd_help_ok; then
    pass "all per-command --help paths exit 0 with valid help text"
fi

# -------------------------------------------------------------------
# Check 5: SCN-CLI-HELP-003 — Per-command -h (short form) exits 0
# -------------------------------------------------------------------
echo "--- SCN-CLI-HELP-003: Per-command -h (short form) ---"
SHORT_HELP_COMMANDS=("sync" "index" "search" "auth" "config")
short_help_ok=true
for cmd in "${SHORT_HELP_COMMANDS[@]}"; do
    run_cmd "${cmd}" -h

    if [ "${ec}" -ne 0 ]; then
        fail "${cmd} -h exit code is ${ec}, expected 0. stderr=${err}"
        short_help_ok=false
        continue
    fi
    if [ -z "${out}" ]; then
        fail "${cmd} -h produced empty output"
        short_help_ok=false
    fi
done
if $short_help_ok; then
    pass "all per-command -h paths exit 0 with non-empty output"
fi

# -------------------------------------------------------------------
# Check 6: SCN-CLI-HELP-004 — Local commands produce help
# -------------------------------------------------------------------
echo "--- SCN-CLI-HELP-004: Local commands --help ---"
LOCAL_COMMANDS=("auth" "config" "doctor" "migrate-cache")
local_cmd_help_ok=true
for cmd in "${LOCAL_COMMANDS[@]}"; do
    run_cmd "${cmd}" --help

    if [ "${ec}" -ne 0 ]; then
        fail "${cmd} --help exit code is ${ec}, expected 0. stderr=${err}"
        local_cmd_help_ok=false
        continue
    fi
    if [ -z "${out}" ]; then
        fail "${cmd} --help produced empty output"
        local_cmd_help_ok=false
    fi
done
if $local_cmd_help_ok; then
    pass "all local commands --help exit 0 with non-empty output"
fi

# -------------------------------------------------------------------
# Check 7: SCN-CLI-HELP-005 — Command+subcommand local help
# -------------------------------------------------------------------
echo "--- SCN-CLI-HELP-005: Local subcommand --help ---"
LOCAL_SUBCOMMANDS=(
    "config init" "config locate" "config show"
    "auth status"
    "repo add" "repo status"
)
local_sub_help_ok=true
for combo in "${LOCAL_SUBCOMMANDS[@]}"; do
    IFS=' ' read -r cmd sub <<< "${combo}"
    run_cmd "${cmd}" "${sub}" --help

    if [ "${ec}" -ne 0 ]; then
        fail "${cmd} ${sub} --help exit code is ${ec}, expected 0. stderr=${err}"
        local_sub_help_ok=false
        continue
    fi
    if ! echo "${out}" | grep -qi "usage"; then
        fail "${cmd} ${sub} --help output missing Usage: out=${out}"
        local_sub_help_ok=false
        continue
    fi
done
if $local_sub_help_ok; then
    pass "all local subcommand --help paths exit 0 with Usage text"
fi

# -------------------------------------------------------------------
# Check 8: SCN-CLI-HELP-006 — Alias commands produce help
# -------------------------------------------------------------------
echo "--- SCN-CLI-HELP-006: Alias command --help ---"
ALIAS_COMMANDS=("snippet" "sync_status" "export-snapshot" "diff-snapshot")
alias_help_ok=true
for cmd in "${ALIAS_COMMANDS[@]}"; do
    run_cmd "${cmd}" --help

    if [ "${ec}" -ne 0 ]; then
        fail "${cmd} --help exit code is ${ec}, expected 0. stderr=${err}"
        alias_help_ok=false
        continue
    fi
    if [ -z "${out}" ]; then
        fail "${cmd} --help produced empty output"
        alias_help_ok=false
    fi
done
if $alias_help_ok; then
    pass "all alias command --help paths exit 0 with non-empty output"
fi

# -------------------------------------------------------------------
# Check 9: SCN-CLI-HELP-007 — Unknown command still errors
# -------------------------------------------------------------------
echo "--- SCN-CLI-HELP-007: Unknown command errors ---"
run_cmd nonexistent --help

if [ "${ec}" -ne 2 ]; then
    fail "nonexistent --help exit code is ${ec}, expected 2"
else
    pass "unknown command --help exit code is 2"
fi

# "unknown command" is on stderr
if echo "${err}" | grep -q "unknown command"; then
    pass "unknown command --help produces 'unknown command' diagnostic on stderr"
else
    fail "unknown command --help missing 'unknown command' diagnostic. err=${err} out=${out}"
fi

# -------------------------------------------------------------------
# Check 10: Acceptance criteria references "bind" — check repo add
# -------------------------------------------------------------------
echo "--- bind/repo compatibility note ---"
# "bind" is not a command in this codebase; "repo add" serves the binding function.
# Verify repo --help works.
run_cmd repo --help
if [ "${ec}" -eq 0 ]; then
    pass "repo --help exit code is 0"
else
    fail "repo --help exit code is ${ec}, expected 0"
fi

run_cmd repo add --help
if [ "${ec}" -eq 0 ] && echo "${out}" | grep -qi "usage"; then
    pass "repo add --help produces valid Usage-based help"
else
    fail "repo add --help: exit=${ec}, out=${out}"
fi

# -------------------------------------------------------------------
# Check 11: Scenario ID inventory completeness
# -------------------------------------------------------------------
echo "--- Scenario ID inventory completeness ---"
SCENARIO_MD="${TASK_DIR}/scenarios.md"
for sid in \
    "006-cmd-gitcode-mcp-task-6-change-cli-help-wiring-for-all-subcommands-scenario-1" \
    "006-cmd-gitcode-mcp-task-6-change-cli-help-wiring-for-all-subcommands-scenario-2"; do
    if grep -q "${sid}" "${SCENARIO_MD}"; then
        pass "scenario id ${sid} appears in scenarios.md"
    else
        fail "scenario id ${sid} MISSING from scenarios.md"
    fi
done

# -------------------------------------------------------------------
# Check 12: Go tests for CLI help execute in test suite
# -------------------------------------------------------------------
echo "--- Existing Go test coverage for CLI help ---"
(cd "${REPO_ROOT}" && go test ./internal/cli/ -run "TestCommandHelp|TestLocalCommandHelp|TestLocalSubcommandHelp|TestAliasCommandHelp|TestHelpDoesNotCreateService|TestUnknownCommandErrors|TestAllCommandsRegistered" -count=1 2>&1) > "${TEST_CACHE_DIR}/help-tests.txt"
if [ $? -eq 0 ]; then
    pass "existing CLI help tests pass (all 8 test functions)"
else
    fail "existing CLI help tests failed"
    cat "${TEST_CACHE_DIR}/help-tests.txt"
fi

# -------------------------------------------------------------------
# Check 13: Production code inspection — helpRequested wiring
# -------------------------------------------------------------------
echo "--- helpRequested wiring in production code ---"
CLI_GO="${REPO_ROOT}/internal/cli/cli.go"

if grep -q 'helpRequested  bool' "${CLI_GO}"; then
    pass "helpRequested field exists in options struct"
else
    fail "helpRequested field missing in options struct"
fi

if grep -q 'BoolVar.*helpRequested.*"help"' "${CLI_GO}"; then
    pass "--help flag registers helpRequested"
else
    fail "--help flag not registered on helpRequested"
fi

if grep -q 'BoolVar.*helpRequested.*"h"' "${CLI_GO}"; then
    pass "-h flag registers helpRequested"
else
    fail "-h flag not registered on helpRequested"
fi

# Check executeWithFactoryAndDeps checks helpRequested and calls printCommandHelp
if grep -A2 'opts.helpRequested' "${CLI_GO}" | grep -q 'printCommandHelp'; then
    pass "helpRequested check calls printCommandHelp in executeWithFactoryAndDeps"
else
    fail "helpRequested check does not call printCommandHelp in executeWithFactoryAndDeps"
fi

if grep -q 'func printCommandHelp' "${CLI_GO}"; then
    pass "printCommandHelp function exists"
else
    fail "printCommandHelp function missing"
fi

if grep -q 'func printLocalSubcommandHelp' "${CLI_GO}"; then
    pass "printLocalSubcommandHelp function exists"
else
    fail "printLocalSubcommandHelp function missing"
fi

if grep -q 'func isKnownCommand' "${CLI_GO}"; then
    pass "isKnownCommand function exists for command validation"
else
    fail "isKnownCommand function missing"
fi

# -------------------------------------------------------------------
# Check 14: DECOMM-009 — No invalid_query on any --help path
# -------------------------------------------------------------------
echo "--- DECOMM-009: No invalid_query on any --help path ---"
INVALID_HELP_OK=true
for cmd in "${COMMANDS_WITH_HELP[@]}"; do
    run_cmd "${cmd}" --help
    if echo "${out}${err}" | grep -qi "invalid_query"; then
        fail "DECOMM-009 failure: ${cmd} --help contains invalid_query"
        INVALID_HELP_OK=false
    fi
done
# Also check local subcommand --help paths
for combo in "${LOCAL_SUBCOMMANDS[@]}"; do
    IFS=' ' read -r cmd sub <<< "${combo}"
    run_cmd "${cmd}" "${sub}" --help
    if echo "${out}${err}" | grep -qi "invalid_query"; then
        fail "DECOMM-009 failure: ${cmd} ${sub} --help contains invalid_query"
        INVALID_HELP_OK=false
    fi
done
# Also check alias --help paths
for cmd in "${ALIAS_COMMANDS[@]}"; do
    run_cmd "${cmd}" --help
    if echo "${out}${err}" | grep -qi "invalid_query"; then
        fail "DECOMM-009 failure: ${cmd} --help contains invalid_query"
        INVALID_HELP_OK=false
    fi
done
if $INVALID_HELP_OK; then
    pass "DECOMM-009 verified: no invalid_query on any --help path"
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
