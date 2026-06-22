#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "${ROOT_DIR}"

export GITCODE_LIVE_TEST=""
export GITCODE_TOKEN=""
unset GITCODE_REPO_ID GITCODE_E2E_REPO_ID

echo "[020-internal-index-task-2-write-indexed_at-into-chunk-metadata-scenario-1] chunk determinism keeps indexed_at as non-zero RFC3339Nano metadata"
go test ./internal/index/ -run '^TestChunkDeterminism$' -count=1

echo "[020-internal-index-task-2-write-indexed_at-into-chunk-metadata-scenario-2] freshness reconstruction reads indexed_at from chunk metadata"
go test ./internal/index/ -run '^TestFreshnessReportClassifications$' -count=1

echo "[020-internal-index-task-2-write-indexed_at-into-chunk-metadata-scenario-3] chunk policy metadata includes indexed_at without breaking deterministic fields"
go test ./internal/index/ -run '^TestChunkPolicyDeterminismAndMetadata$' -count=1

echo "[020-internal-index-task-2-write-indexed_at-into-chunk-metadata] internal index package regression"
go test ./internal/index/ -count=1
