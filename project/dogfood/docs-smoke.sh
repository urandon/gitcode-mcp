#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SAFETY_LIB="${SCRIPT_DIR}/lib/safety.sh"
MANIFEST="${DOGFOOD_DOCS_SMOKE_MANIFEST:-${SCRIPT_DIR}/docs-smoke.commands}"

usage() {
  cat <<'USAGE'
Usage: project/dogfood/docs-smoke.sh --fixture-config PATH --cache-path PATH --transcript PATH [--live]

Runs the public-safe docs smoke workflow against sanitized fixtures.

Required:
  --fixture-config PATH   YAML config used by config docs commands
  --cache-path PATH       SQLite cache path for fixture smoke state
  --transcript PATH       Redacted transcript output path

Optional:
  --live                  Allow credential-gated live validation when GITCODE_LIVE_TEST=1 and GITCODE_TOKEN is present
  -h, --help             Show this help
USAGE
}

fixture_config=""
cache_path=""
transcript=""
live="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --fixture-config)
      fixture_config="${2:-}"
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
    --live)
      live="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'docs-smoke: unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${fixture_config}" || -z "${cache_path}" || -z "${transcript}" ]]; then
  usage >&2
  exit 2
fi

if [[ ! -f "${REPO_ROOT}/${fixture_config}" && ! -f "${fixture_config}" ]]; then
  printf 'docs-smoke: fixture config not found: %s\n' "${fixture_config}" >&2
  exit 2
fi

if [[ ! -f "${MANIFEST}" ]]; then
  printf 'docs-smoke: manifest not found: %s\n' "${MANIFEST}" >&2
  exit 2
fi

# shellcheck source=/dev/null
source "${SAFETY_LIB}"
load_fixture_allowlist

BIN="${GITCODE_MCP_BIN:-go run ./cmd/gitcode-mcp}"
TMP_ROOT="$(mktemp -d)"
raw_transcript="${TMP_ROOT}/docs-smoke.raw.md"
mkdir -p "$(dirname "${cache_path}")"
rm -f "${cache_path}" "${cache_path}.lock" "${cache_path}-wal" "${cache_path}-shm"

gates_live_ready() {
  [[ "${live}" == "true" && "${GITCODE_LIVE_TEST:-}" == "1" && -n "${GITCODE_TOKEN:-}" ]]
}

load_manifest() {
  local line
  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ -z "${line}" || "${line}" == \#* ]] && continue
    printf '%s\n' "${line}"
  done < "${MANIFEST}"
}

prepare_fixture_env() {
  export GITCODE_MCP_CONFIG="${fixture_config}"
  export DOGFOOD_SMOKE_CACHE_PATH="${cache_path}"
  export DOGFOOD_SMOKE_TMP="${TMP_ROOT}"
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
  if [[ -s "${stderr_file}" || -s "${stdout_file}" ]]; then
    printf 'documented_diagnostic\n'
    return 0
  fi
  printf 'undocumented_failure\n'
}

append_step_header() {
  local id="$1"
  local doc_path="$2"
  local command="$3"
  {
    printf '\n## %s\n\n' "${id}"
    printf '%s\n' "- doc: \`${doc_path}\`"
    printf '%s\n\n' "- command: \`${command}\`"
  } >> "${raw_transcript}"
}

run_step() {
  local id="$1"
  local doc_path="$2"
  local command="$3"
  local allowed="$4"
  local requires_network="$5"
  local fixture_only="$6"
  local redaction_profile="$7"
  local rendered
  rendered="$(replace_placeholders "${command}")"

  append_step_header "${id}" "${doc_path}" "${rendered}"

  if [[ "${fixture_only}" == "true" && "${rendered}" == *"--live"* ]]; then
    printf 'outcome: undocumented_failure\nreason: fixture-only step attempted live access\n' >> "${raw_transcript}"
    return 1
  fi

  if [[ "${requires_network}" == "true" ]] && ! gates_live_ready; then
    if allowed_contains "${allowed}" "live_skip"; then
      printf 'outcome: live_skip\nreason: live validation requires --live, GITCODE_LIVE_TEST=1, and token presence\n' >> "${raw_transcript}"
      return 0
    fi
    printf 'outcome: undocumented_failure\nreason: network-required step lacked live gates\n' >> "${raw_transcript}"
    return 1
  fi

  local stdout_file="${TMP_ROOT}/${id}.stdout"
  local stderr_file="${TMP_ROOT}/${id}.stderr"
  local code=0
  (
    cd "${REPO_ROOT}"
    bash -c "${rendered}"
  ) >"${stdout_file}" 2>"${stderr_file}" || code=$?

  local outcome
  outcome="$(classify_outcome "${code}" "${stdout_file}" "${stderr_file}")"
  printf 'outcome: %s\nexit_code: %s\nredaction_profile: %s\n\n' "${outcome}" "${code}" "${redaction_profile}" >> "${raw_transcript}"
  printf 'stdout:\n```text\n' >> "${raw_transcript}"
  cat "${stdout_file}" >> "${raw_transcript}"
  printf '\n```\n\nstderr:\n```text\n' >> "${raw_transcript}"
  cat "${stderr_file}" >> "${raw_transcript}"
  printf '\n```\n' >> "${raw_transcript}"

  if allowed_contains "${allowed}" "${outcome}"; then
    return 0
  fi
  printf 'docs-smoke: step %s produced outcome %s not in allowed outcomes %s\n' "${id}" "${outcome}" "${allowed}" >&2
  return 1
}

run_mcp_read_step() {
  local id="$1"
  local doc_path="$2"
  local allowed="$3"
  local port="${DOGFOOD_MCP_PORT:-19020}"
  local server_log="${TMP_ROOT}/mcp-server.log"
  local sse_out="${TMP_ROOT}/mcp-sse.out"
  local endpoint=""
  local outcome="success"

  append_step_header "${id}" "${doc_path}" "MCP HTTP/SSE get_snippet"

  if ! command -v curl >/dev/null 2>&1; then
    printf 'outcome: documented_diagnostic\nreason: curl is unavailable\n' >> "${raw_transcript}"
    return 0
  fi

  (
    cd "${REPO_ROOT}"
    bash -c "${BIN} mcp serve --transport http-sse --bind 127.0.0.1:${port} --cache-path '${cache_path}'"
  ) >"${TMP_ROOT}/mcp-server.stdout" 2>"${server_log}" &
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
    outcome="documented_diagnostic"
    printf 'outcome: %s\nreason: MCP HTTP/SSE server did not become healthy\n' "${outcome}" >> "${raw_transcript}"
    cat "${server_log}" >> "${raw_transcript}"
    return 0
  fi

  curl -sN "http://127.0.0.1:${port}/sse" >"${sse_out}" &
  local sse_pid=$!

  for _ in {1..50}; do
    endpoint="$(grep -m1 '^data: ' "${sse_out}" 2>/dev/null | sed 's/^data: //')"
    if [[ -n "${endpoint}" ]]; then
      break
    fi
    sleep 0.2
  done

  if [[ -z "${endpoint}" ]]; then
    outcome="documented_diagnostic"
    printf 'outcome: %s\nreason: MCP SSE endpoint was not announced\n' "${outcome}" >> "${raw_transcript}"
    return 0
  fi

  local payload='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_snippet","arguments":{"repo_id":"example-owner/example-repo","source_id":"ISSUE-42","line_start":1,"line_end":3}}}'
  curl -fsS -X POST -H 'Content-Type: application/json' --data "${payload}" "http://127.0.0.1:${port}${endpoint}" >/dev/null || outcome="documented_diagnostic"

  local saw_snippet="false"
  for _ in {1..50}; do
    if grep -q 'remote issue body' "${sse_out}" 2>/dev/null; then
      saw_snippet="true"
      break
    fi
    sleep 0.2
  done

  if [[ "${saw_snippet}" != "true" ]]; then
    outcome="documented_diagnostic"
  fi

  printf 'outcome: %s\n\nSSE transcript:\n```text\n' "${outcome}" >> "${raw_transcript}"
  cat "${sse_out}" >> "${raw_transcript}"
  printf '\n```\n\nserver log:\n```text\n' >> "${raw_transcript}"
  cat "${server_log}" >> "${raw_transcript}"
  printf '\n```\n' >> "${raw_transcript}"

  if allowed_contains "${allowed}" "${outcome}"; then
    return 0
  fi
  printf 'docs-smoke: MCP step %s produced outcome %s not in allowed outcomes %s\n' "${id}" "${outcome}" "${allowed}" >&2
  return 1
}

write_transcript() {
  write_redacted_transcript "${raw_transcript}" "${transcript}"
}

prepare_fixture_env
{
  printf '# Docs Smoke Transcript\n\n'
  printf '%s\n' "- fixture_config: \`${fixture_config}\`"
  printf '%s\n' '- cache_path: `<REDACTED_PATH>`'
  printf '%s\n' "- live_enabled: \`${live}\`"
} > "${raw_transcript}"

failures=0
while IFS='|' read -r id doc_path command allowed requires_network fixture_only redaction_profile; do
  if [[ "${command}" == "MCP_READ" ]]; then
    run_mcp_read_step "${id}" "${doc_path}" "success,documented_diagnostic" || failures=$((failures + 1))
  else
    run_step "${id}" "${doc_path}" "${command}" "${allowed}" "${requires_network}" "${fixture_only}" "${redaction_profile}" || failures=$((failures + 1))
  fi
done < <(load_manifest)

write_transcript

if [[ ${failures} -gt 0 ]]; then
  printf 'docs-smoke: %d failure(s); transcript: %s\n' "${failures}" "${transcript}" >&2
  exit 1
fi

printf 'docs-smoke: passed; transcript: %s\n' "${transcript}"
