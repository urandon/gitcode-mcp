#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

export GONOSUMDB="${GONOSUMDB:-*}"
unset GITCODE_TEST_TOKEN GITCODE_TOKEN

printf '==> index pipeline validation\n'
go test ./internal/index/... -run TestIndexPipeline -count=1

printf '==> chunk determinism validation\n'
go test ./internal/index/... -run TestChunkDeterminism -count=1

printf '==> citation anchor validation\n'
go test ./internal/index/... -run TestCitationAnchors -count=1

printf '==> parser and link edge-case validation\n'
go test ./internal/index/... -run TestParserLinkEdgeCases -count=1

printf '==> offline short-suite validation\n'
go test ./... -short -count=1
