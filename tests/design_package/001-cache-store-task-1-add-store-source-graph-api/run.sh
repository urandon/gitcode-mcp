#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

export GONOSUMDB="${GONOSUMDB:-*}"

printf '==> focused cache source graph validation\n'
go test ./internal/cache/... -run 'TestBacklinks|TestChunkIdentity|TestIdentityResolution|TestSourceGraphRollback' -count=1

printf '==> repository compile and regression validation\n'
go test ./... -count=1
