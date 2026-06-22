#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

export GITCODE_LIVE_TEST=0
unset GITCODE_TOKEN GITCODE_E2E_REPO_ID GITCODE_E2E_OWNER GITCODE_E2E_REPO || true

run() {
  printf '==> %s\n' "$*"
  "$@"
}

run go test ./internal/cache -run 'TestMinimumReplacementCacheState|TestSearchFallbackParity' -count=1
run go test ./internal/service -run 'TestSearchSources|TestFixtureProviderSearchSourcesSmoke' -count=1
run go test ./internal/mcp -run TestIntegration -count=1
run go test ./... 
run git diff --check
