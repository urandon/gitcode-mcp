#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

export GITCODE_TOKEN=""
export GITCODE_MCP_TOKEN=""
export GITCODE_ACCESS_TOKEN=""
export GITCODE_KEYCHAIN_TOKEN=""
export GITCODE_MCP_LIVE_VALIDATION=""
export GITCODE_MCP_DEVICE_VALIDATION=""

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

run_log="$tmpdir/go-test.log"

go test ./internal/diagnostics ./internal/service ./internal/gitcode ./internal/provider/live ./internal/doctor ./internal/cli | tee "$run_log"
go test ./... | tee -a "$run_log"

required=(
  "TestClassifierLivePrecedenceAndHTTPInvariants"
  "TestClassifierUnsupportedPayloadContextRedacted"
  "TestScenario004SelectedBaseURLOnly"
)
for name in "${required[@]}"; do
  if ! go test ./... -run "$name" -count=1 >/dev/null; then
    echo "required validation test failed or missing: $name" >&2
    exit 1
  fi
done

if grep -E "(Authorization:|Bearer [A-Za-z0-9._-]+|token=secret-token|fixture client is read-only.*fixture_fallback_detected)" "$run_log" >/dev/null; then
  echo "validation output leaked credential-like material or raw fixture fallback text" >&2
  exit 1
fi
