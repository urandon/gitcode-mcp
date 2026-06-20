#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SAFETY_LIB="${SCRIPT_DIR}/lib/safety.sh"

usage() {
  cat <<'USAGE'
Usage: project/dogfood/validate-fixtures.sh --fixtures PATH --transcript PATH [--live]

Runs fixture validation gate with six outcome classifications.
Offline validation runs first (always). Live validation runs only when
explicitly gated by --live, GITCODE_LIVE_TEST=1, and GITCODE_TOKEN presence.

Required:
  --fixtures PATH       Fixture directory path
  --transcript PATH     Redacted transcript output path

Optional:
  --live                Enable credential-gated live validation
  -h, --help           Show this help
USAGE
}

fixtures_path=""
transcript=""
live="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --fixtures)
      fixtures_path="${2:-}"
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
      printf 'validate-fixtures: unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${fixtures_path}" || -z "${transcript}" ]]; then
  usage >&2
  exit 2
fi

# shellcheck source=/dev/null
source "${SAFETY_LIB}"
load_fixture_allowlist

TMP_ROOT="$(mktemp -d)"
raw_transcript="${TMP_ROOT}/validate-fixtures.raw.md"

classify_live_outcome() {
  local code="$1"
  local live_stdout="$2"
  local live_stderr="$3"

  if [[ "${code}" == "0" ]]; then
    printf 'live_pass_redacted\n'
    return 0
  fi

  if [[ -s "${live_stdout}" || -s "${live_stderr}" ]]; then
    printf 'live_fail_redacted\n'
    return 0
  fi

  printf 'live_fail_redacted\n'
}

run_live_validation() {
  local live_stdout="${TMP_ROOT}/live.stdout"
  local live_stderr="${TMP_ROOT}/live.stderr"
  local live_code=0

  printf '### live_validation\n\n' >> "${raw_transcript}"

  (
    cd "${REPO_ROOT}"
    GITCODE_LIVE_TEST=1 go test -run Live -count=1 ./internal/gitcode/
  ) >"${live_stdout}" 2>"${live_stderr}" || live_code=$?

  local live_outcome
  live_outcome="$(classify_live_outcome "${live_code}" "${live_stdout}" "${live_stderr}")"
  printf 'outcome: %s\n' "${live_outcome}" >> "${raw_transcript}"
  printf 'exit_code: %s\n\n' "${live_code}" >> "${raw_transcript}"

  printf 'stdout:\n```text\n' >> "${raw_transcript}"
  cat "${live_stdout}" >> "${raw_transcript}"
  printf '\n```\n\nstderr:\n```text\n' >> "${raw_transcript}"
  cat "${live_stderr}" >> "${raw_transcript}"
  printf '\n```\n' >> "${raw_transcript}"
}

gates_live_ready() {
  [[ "${live}" == "true" && "${GITCODE_LIVE_TEST:-}" == "1" && -n "${GITCODE_TOKEN:-}" ]]
}

{
  printf '# Fixture Validation Transcript\n\n'
  printf '%s\n' "- fixtures_path: \`${fixtures_path}\`"
  printf '%s\n' "- live_enabled: \`${live}\`"
  printf '%s\n' "- GITCODE_LIVE_TEST: \`${GITCODE_LIVE_TEST:-unset}\`"
  printf '%s\n' '- GITCODE_TOKEN: `<REDACTED_TOKEN>`'
} > "${raw_transcript}"

printf '\n## offline_validation\n\n' >> "${raw_transcript}"

offline_stdout="${TMP_ROOT}/offline.stdout"
offline_stderr="${TMP_ROOT}/offline.stderr"
offline_code=0

(
  cd "${REPO_ROOT}"
  go test ./... -count=1
) >"${offline_stdout}" 2>"${offline_stderr}" || offline_code=$?

offline_outcome="offline_fail"
if [[ "${offline_code}" == "0" ]]; then
  offline_outcome="offline_pass"
fi

printf 'outcome: %s\n' "${offline_outcome}" >> "${raw_transcript}"
printf 'exit_code: %s\n\n' "${offline_code}" >> "${raw_transcript}"

printf 'stdout:\n```text\n' >> "${raw_transcript}"
cat "${offline_stdout}" >> "${raw_transcript}"
printf '\n```\n\nstderr:\n```text\n' >> "${raw_transcript}"
cat "${offline_stderr}" >> "${raw_transcript}"
printf '\n```\n' >> "${raw_transcript}"

if gates_live_ready; then
  run_live_validation
else
  printf '### live_validation\n\n' >> "${raw_transcript}"
  if [[ "${live}" != "true" ]]; then
    printf 'outcome: live_skipped_no_flag\nreason: --live flag not set\n' >> "${raw_transcript}"
  elif [[ "${GITCODE_LIVE_TEST:-}" != "1" ]]; then
    printf 'outcome: live_skipped_no_flag\nreason: GITCODE_LIVE_TEST not set to 1\n' >> "${raw_transcript}"
  else
    printf 'outcome: live_skipped_no_token\nreason: GITCODE_TOKEN is not set\n' >> "${raw_transcript}"
  fi
fi

write_redacted_transcript "${raw_transcript}" "${transcript}"
transcript_rc=$?

printf 'validate-fixtures: offline_outcome=%s transcript=%s\n' "${offline_outcome}" "${transcript}" >&2

if [[ "${offline_outcome}" == "offline_fail" ]]; then
  printf 'validate-fixtures: offline_fail; transcript: %s\n' "${transcript}" >&2
  exit 1
fi

exit ${transcript_rc}
