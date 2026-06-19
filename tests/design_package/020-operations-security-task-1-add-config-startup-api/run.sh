#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

export GIT_TERMINAL_PROMPT=0
export GOPROXY=off
export GONOSUMDB='*'
unset GITCODE_TOKEN
unset GITCODE_CONFIG

go test ./internal/... -run TestConfigLoading -count=1
go test ./internal/... -run TestCLIFlagOverride -count=1
go test ./... -count=1
git diff --check
