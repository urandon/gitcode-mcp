#!/usr/bin/env bash
set -euo pipefail

SAFETY_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOGFOOD_DIR="$(cd "${SAFETY_LIB_DIR}/.." && pwd)"
ALLOWLIST_FILE="${DOGFOOD_ALLOWLIST:-${DOGFOOD_DIR}/fixture-allowlist.txt}"

declare -a ALLOWLIST_IDS
declare -a ALLOWLIST_HOSTS
declare -a ALLOWLIST_PLACEHOLDERS

load_fixture_allowlist() {
  ALLOWLIST_IDS=()
  ALLOWLIST_HOSTS=()
  ALLOWLIST_PLACEHOLDERS=()
  local section=""

  if [[ ! -f "${ALLOWLIST_FILE}" ]]; then
    printf "safety: allowlist file not found: %s\n" "${ALLOWLIST_FILE}" >&2
    return 2
  fi

  while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%%#*}"
    line="${line//$'\r'/}"
    if [[ -z "${line}" ]]; then
      continue
    fi
    if [[ "${line}" =~ ^\[(.*)\]$ ]]; then
      section="${BASH_REMATCH[1]}"
      continue
    fi
    case "${section}" in
      fixture_ids)
        ALLOWLIST_IDS+=("${line}")
        ;;
      fixture_hosts)
        ALLOWLIST_HOSTS+=("${line}")
        ;;
      placeholders)
        ALLOWLIST_PLACEHOLDERS+=("${line}")
        ;;
    esac
  done < "${ALLOWLIST_FILE}"

  if [[ ${#ALLOWLIST_PLACEHOLDERS[@]} -eq 0 ]]; then
    ALLOWLIST_PLACEHOLDERS=(
      "<REDACTED_TOKEN>"
      "<REDACTED_SECRET>"
      "<REDACTED_PATH>"
      "<REDACTED_HOST>"
      "<REDACTED_RESPONSE>"
      "<FIXTURE_REPO>"
    )
  fi
}

redact_transcript() {
  local input="$1"
  local output="${input}"

  if [[ ${#ALLOWLIST_PLACEHOLDERS[@]} -eq 0 ]]; then
    load_fixture_allowlist
  fi

  output="${output//${HOME}/<REDACTED_PATH>}"
  output="${output//${TMPDIR:-/tmp}/<REDACTED_PATH>}"

  if [[ -n "${GITCODE_TOKEN:-}" ]]; then
    output="${output//${GITCODE_TOKEN}/<REDACTED_TOKEN>}"
  fi

  printf '%s\n' "${output}"
}

assert_public_safe_transcript() {
  local file="$1"
  local violations=0

  if [[ ! -f "${file}" ]]; then
    printf "safety: transcript file not found: %s\n" "${file}" >&2
    return 2
  fi

  if grep -qE 'Bearer [A-Za-z0-9+/=_-]{20,}' "${file}" 2>/dev/null; then
    printf "safety: VIOLATION: bearer token found in %s\n" "${file}" >&2
    violations=$((violations + 1))
  fi

  if grep -qE 'Authorization: (Bearer|Basic|token)' "${file}" 2>/dev/null; then
    printf "safety: VIOLATION: authorization header found in %s\n" "${file}" >&2
    violations=$((violations + 1))
  fi

  if grep -qE '(Cookie|Set-Cookie):' "${file}" 2>/dev/null; then
    printf "safety: VIOLATION: cookie header found in %s\n" "${file}" >&2
    violations=$((violations + 1))
  fi

  if grep -qE '^[a-zA-Z0-9+/=]{40,}$' "${file}" 2>/dev/null; then
    printf "safety: VIOLATION: high-entropy token-like string found in %s\n" "${file}" >&2
    violations=$((violations + 1))
  fi

  if grep -qE '(^|[^"'"'"'A-Za-z0-9_.-])/Users/[a-zA-Z]' "${file}" 2>/dev/null; then
    printf "safety: VIOLATION: absolute user path found in %s\n" "${file}" >&2
    violations=$((violations + 1))
  fi

  if grep -qE '(^|[^"'"'"'A-Za-z0-9_.-])/home/[a-zA-Z]' "${file}" 2>/dev/null; then
    printf "safety: VIOLATION: Linux home path found in %s\n" "${file}" >&2
    violations=$((violations + 1))
  fi

  if grep -qiE 'C:\\Users\\' "${file}" 2>/dev/null; then
    printf "safety: VIOLATION: Windows profile path found in %s\n" "${file}" >&2
    violations=$((violations + 1))
  fi

  if [[ ${violations} -gt 0 ]]; then
    printf "safety: %d public-safety violation(s) in %s\n" "${violations}" "${file}" >&2
    return 1
  fi
  return 0
}

write_redacted_transcript() {
  local input="$1"
  local output_file="$2"

  if [[ ! -f "${input}" ]]; then
    printf "safety: input transcript file not found: %s\n" "${input}" >&2
    return 2
  fi

  local outdir
  outdir="$(dirname "${output_file}")"
  mkdir -p "${outdir}"

  local content
  content="$(cat "${input}")"
  content="$(redact_transcript "${content}")"
  printf '%s\n' "${content}" > "${output_file}"

  if ! assert_public_safe_transcript "${output_file}"; then
    printf "safety: redacted transcript failed public-safety check: %s\n" "${output_file}" >&2
    return 1
  fi

  printf "safety: redacted transcript written to %s\n" "${output_file}" >&2
  return 0
}
