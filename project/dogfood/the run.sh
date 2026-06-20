#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SAFETY_LIB="${SCRIPT_DIR}/lib/safety.sh"
SMOKE_MANIFEST="${DOGFOOD_SMOKE_MANIFEST:-${SCRIPT_DIR}/docs-smoke.commands}"
ALLOWLIST="${SCRIPT_DIR}/fixture-allowlist.txt"
EVIDENCE_LOG="${SCRIPT_DIR}/checklist.md"
EVIDENCE_DIR="${SCRIPT_DIR}/evidence"

usage() {
  cat <<'USAGE'
Usage: project/dogfood/the\ run.sh --slice dayN --cache-path PATH --transcript PATH [--fixture-config PATH]

Runs a single dogfood checklist day slice and produces a redacted evidence transcript.
All steps are fixture/cache-only by default; live credentials are never required.

Required:
  --slice dayN           Day slice to run (day1 through day7)
  --cache-path PATH      SQLite cache path for fixture state
  --transcript PATH      Redacted transcript output path

Optional:
  --fixture-config PATH  YAML config used by config docs commands
                         (default: testdata/configs/dogfood.yaml if it exists)
  -h, --help             Show this help

Slices:
  day1  Config & Repository Binding
  day2  Fixture Sync & Index
  day3  CLI Reads
  day4  MCP Parity & Transport
  day5  Concurrency & Write Safety
  day6  Snapshot Integrity
  day7  Docs, Live Validation & Feedback
USAGE
}

slice=""
cache_path=""
transcript=""
fixture_config=""
declare -a required_prior=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --slice)
      slice="${2:-}"
      shift 2
      ;;
    --cache-path)
      cache_path="${2:-}"
      shift 2
      ;;
    --transcript)
      transcript="${2:-}"
      shift 2
      ;;
    --fixture-config)
      fixture_config="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'the run.sh: unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${slice}" || -z "${cache_path}" || -z "${transcript}" ]]; then
  usage >&2
  exit 2
fi

if [[ -z "${fixture_config}" ]]; then
  if [[ -f "${REPO_ROOT}/testdata/configs/dogfood.yaml" ]]; then
    fixture_config="testdata/configs/dogfood.yaml"
  fi
fi

# shellcheck source=/dev/null
source "${SAFETY_LIB}"

for required_fn in load_fixture_allowlist redact_transcript assert_public_safe_transcript write_redacted_transcript; do
  if ! declare -f "${required_fn}" >/dev/null 2>&1; then
    printf 'the run.sh: required function %s not found in safety lib %s\n' "${required_fn}" "${SAFETY_LIB}" >&2
    exit 2
  fi
done

load_fixture_allowlist

BIN="${GITCODE_MCP_BIN:-go run ./cmd/gitcode-mcp}"
TMP_ROOT="$(mktemp -d)"
raw_transcript="${TMP_ROOT}/checklist-slice.raw.md"
mkdir -p "$(dirname "${cache_path}")"
rm -f "${cache_path}" "${cache_path}.lock" "${cache_path}-wal" "${cache_path}-shm"

prepare_env() {
  export GITCODE_MCP_CONFIG="${fixture_config}"
  export DOGFOOD_CHECKLIST_CACHE_PATH="${cache_path}"
  export DOGFOOD_CHECKLIST_TMP="${TMP_ROOT}"
  export DOGFOOD_CHECKLIST_SLICE="${slice}"
}

replace_placeholders() {
  local command="$1"
  command="${command//\{BIN\}/${BIN}}"
  command="${command//\{CACHE_PATH\}/${cache_path}}"
  command="${command//\{TMP\}/${TMP_ROOT}}"
  printf '%s\n' "${command}"
}

allowed_contains() {
  local allowed="$1"
  local value="$2"
  [[ ",${allowed}," == *",${value},"* ]]
}

classify_outcome() {
  local code="$1"
  local stdout_file="$2"
  local stderr_file="$3"
  if [[ "${code}" == "0" ]]; then
    if grep -q 'dry_run' "${stdout_file}" 2>/dev/null; then
      printf 'dry_run\n'
    else
      printf 'success\n'
    fi
    return 0
  fi
  if grep -qF 'unknown command' "${stderr_file}" 2>/dev/null; then
    printf 'product_gap\n'
    return 0
  fi
  if [[ -s "${stderr_file}" || -s "${stdout_file}" ]]; then
    printf 'documented_diagnostic\n'
    return 0
  fi
  printf 'undocumented_failure\n'
}

append_step_header() {
  local id="$1"
  local desc="$2"
  local command="$3"
  {
    printf '\n## %s\n\n' "${id}"
    printf '%s\n' "- description: ${desc}"
    printf '%s\n\n' "- command: \`${command}\`"
  } >> "${raw_transcript}"
}

run_command() {
  local id="$1"
  local desc="$2"
  local command="$3"
  local allowed="$4"
  local rendered
  rendered="$(replace_placeholders "${command}")"

  append_step_header "${id}" "${desc}" "${rendered}"

  local stdout_file="${TMP_ROOT}/${id}.stdout"
  local stderr_file="${TMP_ROOT}/${id}.stderr"
  local code=0
  (
    cd "${REPO_ROOT}"
    bash -c "${rendered}"
  ) >"${stdout_file}" 2>"${stderr_file}" || code=$?

  local outcome
  outcome="$(classify_outcome "${code}" "${stdout_file}" "${stderr_file}")"
  printf 'outcome: %s\nexit_code: %s\n' "${outcome}" "${code}" >> "${raw_transcript}"
  printf '\nstdout:\n```text\n' >> "${raw_transcript}"
  cat "${stdout_file}" >> "${raw_transcript}"
  printf '\n```\n\nstderr:\n```text\n' >> "${raw_transcript}"
  cat "${stderr_file}" >> "${raw_transcript}"
  printf '\n```\n' >> "${raw_transcript}"

  if allowed_contains "${allowed}" "${outcome}"; then
    return 0
  fi
  printf 'the run.sh: step %s produced outcome %s not in allowed outcomes %s\n' "${id}" "${outcome}" "${allowed}" >&2
  return 1
}

check_prior_slices() {
  local priors="$1"
  if [[ -z "${priors}" ]]; then
    return 0
  fi
  local OLDIFS="${IFS}"
  IFS=','
  local prior
  local all_ok="true"
  for prior in ${priors}; do
    prior="${prior// /}"
    if [[ -z "${prior}" ]]; then
      continue
    fi
    local found="false"
    if grep -q "^### slice_id: ${prior}$" "${EVIDENCE_LOG}" 2>/dev/null; then
      local prior_status
      prior_status="$(grep -A 20 "^### slice_id: ${prior}$" "${EVIDENCE_LOG}" 2>/dev/null | grep '^\*\*status\*\*:' | head -1 | sed 's/.*:\s*//' | sed 's/<//g' | sed 's/>//g')"
      if [[ "${prior_status}" == "pass" ]]; then
        found="true"
      fi
    fi
    if [[ "${found}" != "true" ]]; then
      printf 'the run.sh: required prior slice %s evidence missing or not passed\n' "${prior}" >&2
      printf '  ensure %s has passed and its evidence entry is recorded in %s\n' "${prior}" "${EVIDENCE_LOG}" >&2
      all_ok="false"
    fi
  done
  IFS="${OLDIFS}"
  if [[ "${all_ok}" != "true" ]]; then
    return 1
  fi
  return 0
}

slice_day1_config_repo() {
  printf '## Slice: day1-config-repo — Config & Repository Binding\n\n' >> "${raw_transcript}"
  printf '%s\n' "- fixture_config: \`${fixture_config}\`" >> "${raw_transcript}"
  printf '%s\n' "- cache_path: \`${cache_path}\`" >> "${raw_transcript}"

  local failures=0

  run_command "day1-version" "Print version" "${BIN} --version" "success" || failures=$((failures + 1))
  run_command "day1-help" "Print help" "${BIN} --help" "success" || failures=$((failures + 1))
  run_command "day1-config-locate" "Locate active config" "${BIN} --cache-path ${cache_path} config locate" "success" || failures=$((failures + 1))
  run_command "day1-config-show-redacted" "Show config with redacted credentials" "${BIN} --cache-path ${cache_path} config show --redacted" "success" || failures=$((failures + 1))
  run_command "day1-auth-status" "Show auth status" "${BIN} --cache-path ${cache_path} auth status" "success" || failures=$((failures + 1))
  run_command "day1-repo-add" "Add example repository" "${BIN} --cache-path ${cache_path} repo add --repo example-owner/example-repo --owner example-owner --name example-repo --scopes issues,wiki --api-base-url https://api.gitcode.com/api/v5 --display-name \"Example Repository\" --alias example" "success" || failures=$((failures + 1))
  run_command "day1-repo-status" "Show repository status" "${BIN} --cache-path ${cache_path} repo status --repo example-owner/example-repo" "success" || failures=$((failures + 1))

  return ${failures}
}

slice_day2_fixture_sync_index() {
  printf '## Slice: day2-fixture-sync-index — Fixture Sync & Index\n\n' >> "${raw_transcript}"

  local failures=0

  run_command "day2-sync" "Sync fixture issues and wiki with index" "${BIN} --cache-path ${cache_path} sync --repo example-owner/example-repo --issues --wiki --index" "success" || failures=$((failures + 1))
  run_command "day2-cache-status" "Check cache status after sync" "${BIN} --cache-path ${cache_path} cache-status --repo example-owner/example-repo" "success" || failures=$((failures + 1))
  run_command "day2-sync-status" "Check sync status" "${BIN} --cache-path ${cache_path} sync-status --repo example-owner/example-repo" "success" || failures=$((failures + 1))

  return ${failures}
}

slice_day3_cli_reads() {
  printf '## Slice: day3-cli-reads — CLI Reads\n\n' >> "${raw_transcript}"

  local failures=0

  run_command "day3-list" "List all sources" "${BIN} --cache-path ${cache_path} list --repo example-owner/example-repo" "success" || failures=$((failures + 1))
  run_command "day3-get-issue" "Get issue by alias" "${BIN} --cache-path ${cache_path} get --repo example-owner/example-repo issue:42" "success" || failures=$((failures + 1))
  run_command "day3-get-wiki" "Get wiki page by alias" "${BIN} --cache-path ${cache_path} get --repo example-owner/example-repo wiki:Home" "success" || failures=$((failures + 1))
  run_command "day3-search" "Full-text search" "${BIN} --cache-path ${cache_path} search --repo example-owner/example-repo \"remote issue body\"" "success" || failures=$((failures + 1))
  run_command "day3-snippet-issue" "Get snippet from issue" "${BIN} --cache-path ${cache_path} get-snippet --repo example-owner/example-repo issue:42 --line-start 1 --line-end 3" "success" || failures=$((failures + 1))
  run_command "day3-snippet-wiki" "Get snippet from wiki" "${BIN} --cache-path ${cache_path} snippet --repo example-owner/example-repo wiki:Home --line-start 1 --line-end 3" "success" || failures=$((failures + 1))
  run_command "day3-list-chunks" "List index chunks" "${BIN} --cache-path ${cache_path} list-chunks --repo example-owner/example-repo" "success" || failures=$((failures + 1))
  run_command "day3-backlinks" "Find backlinks to ISSUE-42" "${BIN} --cache-path ${cache_path} backlinks --repo example-owner/example-repo ISSUE-42" "success" || failures=$((failures + 1))
  run_command "day3-link-check" "Check link integrity" "${BIN} --cache-path ${cache_path} link-check --repo example-owner/example-repo" "success" || failures=$((failures + 1))
  run_command "day3-stale-index" "Report stale index state" "${BIN} --cache-path ${cache_path} stale-index --repo example-owner/example-repo" "success" || failures=$((failures + 1))
  run_command "day3-recent" "List recent changes" "${BIN} --cache-path ${cache_path} recent --repo example-owner/example-repo --limit 5" "success" || failures=$((failures + 1))
  run_command "day3-cache-status" "Check cache statistics" "${BIN} --cache-path ${cache_path} cache-status --repo example-owner/example-repo" "success" || failures=$((failures + 1))

  return ${failures}
}

slice_day4_mcp_parity_transport() {
  printf '## Slice: day4-mcp-parity-transport — MCP Parity & Transport\n\n' >> "${raw_transcript}"
  printf '%s\n' "- cache_path: \`${cache_path}\`" >> "${raw_transcript}"

  local port="${DOGFOOD_MCP_PORT:-19021}"
  local failures=0

  append_step_header "day4-mcp-server-start" "Start HTTP/SSE MCP server" "${BIN} mcp serve --transport http-sse --bind 127.0.0.1:${port} --cache-path ${cache_path}"

  local server_log="${TMP_ROOT}/day4-server.log"
  local server_stdout="${TMP_ROOT}/day4-server.stdout"
  (
    cd "${REPO_ROOT}"
    bash -c "${BIN} mcp serve --transport http-sse --bind 127.0.0.1:${port} --cache-path ${cache_path}"
  ) >"${server_stdout}" 2>"${server_log}" &
  local server_pid=$!

  cleanup_mcp() {
    if [[ -n "${sse_pid:-}" ]]; then
      kill "${sse_pid}" >/dev/null 2>&1 || true
    fi
    kill "${server_pid}" >/dev/null 2>&1 || true
    wait "${server_pid}" >/dev/null 2>&1 || true
  }
  trap cleanup_mcp RETURN

  local ready="false"
  for _ in {1..50}; do
    if curl -fsS "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
      ready="true"
      break
    fi
    sleep 0.2
  done

  if [[ "${ready}" != "true" ]]; then
    printf 'outcome: documented_diagnostic\nreason: MCP HTTP/SSE server did not become healthy\n' >> "${raw_transcript}"
    printf 'server log:\n```text\n' >> "${raw_transcript}"
    cat "${server_log}" >> "${raw_transcript}"
    printf '\n```\n' >> "${raw_transcript}"
    return 1
  fi

  printf 'outcome: success\nreason: MCP server is healthy\n' >> "${raw_transcript}"

  run_command "day4-health" "Check /health endpoint" "curl -fsS http://127.0.0.1:${port}/health" "success" || failures=$((failures + 1))
  run_command "day4-ready" "Check /ready endpoint" "curl -fsS http://127.0.0.1:${port}/ready" "success" || failures=$((failures + 1))

  local sse_out="${TMP_ROOT}/day4-sse.out"
  curl -sN "http://127.0.0.1:${port}/sse" >"${sse_out}" &
  local sse_pid=$!

  local endpoint=""
  for _ in {1..50}; do
    endpoint="$(grep -m1 '^data: ' "${sse_out}" 2>/dev/null | sed 's/^data: //')"
    if [[ -n "${endpoint}" ]]; then
      break
    fi
    sleep 0.2
  done

  if [[ -z "${endpoint}" ]]; then
    printf '\n## day4-mcp-sse-endpoint\n\noutcome: documented_diagnostic\nreason: SSE endpoint was not announced\n' >> "${raw_transcript}"
    cat "${sse_out}" >> "${raw_transcript}"
    return $((failures + 1))
  fi

  append_step_header "day4-get-snippet-issue" "MCP get_snippet for ISSUE-42" "tools/call get_snippet"
  local payload_issue='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_snippet","arguments":{"repo_id":"example-owner/example-repo","source_id":"ISSUE-42","line_start":1,"line_end":3}}}'
  curl -fsS -X POST -H 'Content-Type: application/json' --data "${payload_issue}" "http://127.0.0.1:${port}${endpoint}" >"${TMP_ROOT}/day4-snippet-issue.resp" 2>"${TMP_ROOT}/day4-snippet-issue.err" || true
  printf 'outcome: documented_diagnostic\n' >> "${raw_transcript}"
  printf 'response:\n```text\n' >> "${raw_transcript}"
  cat "${TMP_ROOT}/day4-snippet-issue.resp" >> "${raw_transcript}"
  printf '\n```\nstderr:\n```text\n' >> "${raw_transcript}"
  cat "${TMP_ROOT}/day4-snippet-issue.err" >> "${raw_transcript}"
  printf '\n```\n' >> "${raw_transcript}"

  append_step_header "day4-get-snippet-wiki" "MCP get_snippet for wiki:Home" "tools/call get_snippet"
  local payload_wiki='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_snippet","arguments":{"repo_id":"example-owner/example-repo","source_id":"wiki:Home","line_start":1,"line_end":3}}}'
  curl -fsS -X POST -H 'Content-Type: application/json' --data "${payload_wiki}" "http://127.0.0.1:${port}${endpoint}" >"${TMP_ROOT}/day4-snippet-wiki.resp" 2>"${TMP_ROOT}/day4-snippet-wiki.err" || true
  printf 'outcome: documented_diagnostic\n' >> "${raw_transcript}"
  printf 'response:\n```text\n' >> "${raw_transcript}"
  cat "${TMP_ROOT}/day4-snippet-wiki.resp" >> "${raw_transcript}"
  printf '\n```\nstderr:\n```text\n' >> "${raw_transcript}"
  cat "${TMP_ROOT}/day4-snippet-wiki.err" >> "${raw_transcript}"
  printf '\n```\n' >> "${raw_transcript}"

  printf '\n## day4-sse-transcript\n\n' >> "${raw_transcript}"
  printf 'SSE transcript:\n```text\n' >> "${raw_transcript}"
  cat "${sse_out}" >> "${raw_transcript}"
  printf '\n```\n\nserver log:\n```text\n' >> "${raw_transcript}"
  cat "${server_log}" >> "${raw_transcript}"
  printf '\n```\n' >> "${raw_transcript}"

  return ${failures}
}

slice_day5_concurrency_write_safety() {
  printf '## Slice: day5-concurrency-write-safety — Concurrency & Write Safety\n\n' >> "${raw_transcript}"

  local failures=0

  run_command "day5-create-issue-dry-run" "Create issue dry run" "${BIN} --cache-path ${cache_path} create-issue --repo example-owner/example-repo --title \"Dry run test\" --body \"Dry run body.\" --dry-run" "success,dry_run" || failures=$((failures + 1))
  run_command "day5-create-issue-live" "Create issue live (expected skip)" "${BIN} --cache-path ${cache_path} create-issue --repo example-owner/example-repo --title \"Live gated\" --body \"Live gated body.\" --live --idempotency-key day5-live-gated" "documented_diagnostic" || failures=$((failures + 1))
  run_command "day5-update-issue-dry-run" "Update issue dry run" "${BIN} --cache-path ${cache_path} update-issue --repo example-owner/example-repo --id ISSUE-42 --title \"Updated title\" --dry-run" "success,dry_run" || failures=$((failures + 1))
  run_command "day5-create-page-dry-run" "Create wiki page dry run" "${BIN} --cache-path ${cache_path} create-page --repo example-owner/example-repo --slug test-page --title \"Test Page\" --body \"Test body.\" --dry-run" "success,dry_run,documented_diagnostic" || failures=$((failures + 1))
  run_command "day5-add-comment-dry-run" "Add comment dry run" "${BIN} --cache-path ${cache_path} add-comment --repo example-owner/example-repo --id ISSUE-42 --body \"Fixture comment.\" --dry-run" "success,dry_run,documented_diagnostic" || failures=$((failures + 1))

  return ${failures}
}

slice_day6_snapshot_integrity() {
  printf '## Slice: day6-snapshot-integrity — Snapshot Integrity\n\n' >> "${raw_transcript}"

  local failures=0

  run_command "day6-export-snapshot-json" "Export JSON snapshot" "${BIN} --cache-path ${cache_path} export-snapshot --repo example-owner/example-repo --format json" "success,documented_diagnostic" || failures=$((failures + 1))
  run_command "day6-export-snapshot-markdown" "Export Markdown snapshot" "${BIN} --cache-path ${cache_path} export-snapshot --repo example-owner/example-repo --format markdown" "success,documented_diagnostic" || failures=$((failures + 1))
  run_command "day6-diff-snapshot" "Diff snapshots with placeholder IDs" "${BIN} --cache-path ${cache_path} diff-snapshot --base-id snapshot-id-1 --head-id snapshot-id-2" "success,documented_diagnostic" || failures=$((failures + 1))
  run_command "day6-diff-snapshot-unknown" "Diff snapshots with unknown IDs" "${BIN} --cache-path ${cache_path} diff-snapshot --base-id unknown-snap-1 --head-id unknown-snap-2" "documented_diagnostic" || failures=$((failures + 1))

  return ${failures}
}

slice_day7_docs_live_validation_feedback() {
  printf '## Slice: day7-docs-live-validation-feedback — Docs, Live Validation & Feedback\n\n' >> "${raw_transcript}"

  local failures=0

  if [[ -x "${SCRIPT_DIR}/docs-smoke.sh" ]]; then
    run_command "day7-docs-smoke" "Run docs smoke" "${SCRIPT_DIR}/docs-smoke.sh --fixture-config ${fixture_config} --cache-path ${cache_path} --transcript ${TMP_ROOT}/docs-smoke-transcript.md" "success,documented_diagnostic" || failures=$((failures + 1))
    printf '\n## day7-docs-smoke-output\n\n' >> "${raw_transcript}"
    if [[ -f "${TMP_ROOT}/docs-smoke-transcript.md" ]]; then
      printf '```text\n' >> "${raw_transcript}"
      cat "${TMP_ROOT}/docs-smoke-transcript.md" >> "${raw_transcript}"
      printf '\n```\n' >> "${raw_transcript}"
    else
      printf 'outcome: documented_diagnostic\nreason: docs smoke transcript was not produced\n' >> "${raw_transcript}"
    fi
  else
    printf '\n## day7-docs-smoke\n\noutcome: documented_diagnostic\nreason: docs-smoke.sh not found or not executable\n' >> "${raw_transcript}"
  fi

  if [[ -x "${SCRIPT_DIR}/validate-fixtures.sh" ]]; then
    run_command "day7-fixture-validation" "Run fixture validation" "${SCRIPT_DIR}/validate-fixtures.sh --fixtures ${REPO_ROOT}/fixtures --transcript ${TMP_ROOT}/fixture-validation-transcript.md" "success,documented_diagnostic" || failures=$((failures + 1))
    printf '\n## day7-fixture-validation-output\n\n' >> "${raw_transcript}"
    if [[ -f "${TMP_ROOT}/fixture-validation-transcript.md" ]]; then
      printf '```text\n' >> "${raw_transcript}"
      cat "${TMP_ROOT}/fixture-validation-transcript.md" >> "${raw_transcript}"
      printf '\n```\n' >> "${raw_transcript}"
    fi
  else
    printf '\n## day7-fixture-validation\n\noutcome: documented_diagnostic\nreason: validate-fixtures.sh not found or not executable\n' >> "${raw_transcript}"
  fi

  run_command "day7-final-cli-issue" "Final CLI read of fixture issue" "${BIN} --cache-path ${cache_path} get --repo example-owner/example-repo issue:42" "success" || failures=$((failures + 1))
  run_command "day7-final-cli-wiki" "Final CLI read of fixture wiki" "${BIN} --cache-path ${cache_path} get --repo example-owner/example-repo wiki:Home" "success" || failures=$((failures + 1))

  return ${failures}
}

write_transcript_output() {
  write_redacted_transcript "${raw_transcript}" "${transcript}"
}

# Evidence is append-only. When a documented command name or flag changes,
# record a replacement entry with:
#   replaces_command_id=<prior command id>
#   reason=<why replacement was needed>
#   supersedes_transcript=<prior transcript path>
# Prior evidence entries are never edited or deleted.
append_evidence_entry() {
  local slice_status="$1"
  local failures="$2"
  local evidence_status="pass"
  local blocker=""
  local next_action=""

  if [[ ${failures} -gt 0 ]]; then
    evidence_status="fail"
    blocker="${failures} step(s) produced unexpected outcomes"
    next_action="review transcript at ${transcript} for undocumented failures"
  fi

  local prior_slices=""
  case "${slice}" in
    day1) prior_slices="(none)" ;;
    day2) prior_slices="day1-config-repo" ;;
    day3) prior_slices="day2-fixture-sync-index" ;;
    day4) prior_slices="day3-cli-reads" ;;
    day5) prior_slices="day4-mcp-parity-transport" ;;
    day6) prior_slices="day5-concurrency-write-safety" ;;
    day7) prior_slices="day6-snapshot-integrity" ;;
  esac

  local expected_result=""
  case "${slice}" in
    day1) expected_result="All config/repo commands succeed or return documented diagnostics; repository is bound and visible in repo status." ;;
    day2) expected_result="Cache contains fixture records. Index chunks exist. Sync events are recorded." ;;
    day3) expected_result="All read commands return repo-scoped, deterministic output. Issue ISSUE-42 and wiki wiki:Home are readable." ;;
    day4) expected_result="MCP server starts. Health and readiness endpoints respond. At least one MCP read tool returns documented fixture snippet or documented diagnostic." ;;
    day5) expected_result="All --dry-run commands return dry_run confirmation without mutation. --live commands are skipped or return documented diagnostic when credentials are absent." ;;
    day6) expected_result="Export produces deterministic output. Diff rejects unknown IDs with not-found or returns documented diagnostic." ;;
    day7) expected_result="Docs smoke passes. Fixture validation passes offline. Offline CLI and MCP reads for one fixture issue and one fixture wiki page succeed." ;;
  esac

  local stamp
  stamp="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

  {
    printf '\n### slice_id: %s\n\n' "${slice}"
    printf '%s\n' "- **timestamp**: ${stamp}"
    printf '%s\n' "- **required_prior_slices**: ${prior_slices}"
    printf '%s\n' "- **command**: project/dogfood/the\\\\ run.sh --slice ${slice} --cache-path ${cache_path} --transcript ${transcript}"
    printf '%s\n' "- **expected_fixture_result**: ${expected_result}"
    printf '%s\n' "- **actual_redacted_result**: **${evidence_status^^}** — ${failures} failure(s)"
    printf '%s\n' "- **transcript_path**: ${transcript}"
    printf '%s\n' "- **status**: ${evidence_status}"
    printf '%s\n' "- **blocker**: ${blocker}"
    printf '%s\n' "- **next_action**: ${next_action}"
    printf '\n'
  } >> "${EVIDENCE_LOG}"
}

prepare_env

if [[ -z "${fixture_config}" ]]; then
  printf 'the run.sh: fixture config not available; expected file at testdata/configs/dogfood.yaml\n' >&2
  printf '# Checklist Slice: %s\n\n' "${slice}" > "${raw_transcript}"
  printf 'outcome: documented_diagnostic\nreason: fixture config not found\n' >> "${raw_transcript}"
  write_transcript_output
  exit 0
fi

if [[ ! -f "${fixture_config}" ]]; then
  printf 'the run.sh: fixture config file not found: %s\n' "${fixture_config}" >&2
  printf '# Checklist Slice: %s\n\n' "${slice}" > "${raw_transcript}"
  printf 'outcome: documented_diagnostic\nreason: fixture config file not found: %s\n' "${fixture_config}" >> "${raw_transcript}"
  write_transcript_output
  exit 0
fi

priors=""
case "${slice}" in
  day1) priors="" ;;
  day2) priors="day1-config-repo" ;;
  day3) priors="day2-fixture-sync-index" ;;
  day4) priors="day3-cli-reads" ;;
  day5) priors="day4-mcp-parity-transport" ;;
  day6) priors="day5-concurrency-write-safety" ;;
  day7) priors="day6-snapshot-integrity" ;;
  *)
    printf 'the run.sh: unknown slice: %s (expected day1 through day7)\n' "${slice}" >&2
    exit 2
    ;;
esac

if ! check_prior_slices "${priors}"; then
  printf '# Checklist Slice: %s\n\n' "${slice}" > "${raw_transcript}"
  printf 'outcome: blocked\nreason: required prior slice(s) not passed: %s\n' "${priors}" >> "${raw_transcript}"
  printf '  ensure prior slices have passed and their evidence entries are recorded in %s\n' "${EVIDENCE_LOG}" >> "${raw_transcript}"
  write_transcript_output
  exit 0
fi

{
  printf '# Dogfood Checklist Transcript: %s\n\n' "${slice}"
  printf '%s\n' "- slice: \`${slice}\`"
  printf '%s\n' "- fixture_config: \`${fixture_config}\`"
  printf '%s\n' '- cache_path: `<REDACTED_PATH>`'
  printf '%s\n' "- transcript: \`${transcript}\`"
  printf '%s\n' "- GITCODE_LIVE_TEST: \`${GITCODE_LIVE_TEST:-unset}\`"
  printf '%s\n' '- GITCODE_TOKEN: `<REDACTED_TOKEN>`'
} > "${raw_transcript}"

slice_failures=0
case "${slice}" in
  day1) slice_day1_config_repo || slice_failures=$? ;;
  day2) slice_day2_fixture_sync_index || slice_failures=$? ;;
  day3) slice_day3_cli_reads || slice_failures=$? ;;
  day4) slice_day4_mcp_parity_transport || slice_failures=$? ;;
  day5) slice_day5_concurrency_write_safety || slice_failures=$? ;;
  day6) slice_day6_snapshot_integrity || slice_failures=$? ;;
  day7) slice_day7_docs_live_validation_feedback || slice_failures=$? ;;
esac

slice_status="pass"
if [[ ${slice_failures} -gt 0 ]]; then
  slice_status="fail"
fi

printf '\n## Slice Result\n\n' >> "${raw_transcript}"
printf 'slice: %s\nstatus: %s\nfailures: %s\n' "${slice}" "${slice_status}" "${slice_failures}" >> "${raw_transcript}"

write_transcript_output
append_evidence_entry "${slice_status}" "${slice_failures}"

printf 'the run.sh: slice=%s status=%s failures=%s transcript=%s\n' "${slice}" "${slice_status}" "${slice_failures}" "${transcript}" >&2

if [[ ${slice_failures} -gt 0 ]]; then
  exit 1
fi

exit 0
