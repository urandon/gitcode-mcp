#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "${ROOT_DIR}"

export GITCODE_LIVE_TEST=""
export GITCODE_TOKEN=""
unset GITCODE_REPO_ID GITCODE_E2E_REPO_ID

echo "[019-internal-index-task-1-use-body-hash-for-staleness-in-freshness-scenario-1] fresh body hash matches indexed chunk hash"
echo "[019-internal-index-task-1-use-body-hash-for-staleness-in-freshness-scenario-2] changed body hash differs from indexed chunk hash and misleading metadata does not mask staleness"
go test ./internal/index/ -run '^TestFreshnessReportClassifications$' -count=1

echo "[019-internal-index-task-1-use-body-hash-for-staleness-in-freshness-scenario-3] SourceRecord has no PreviousIndexedHash field and internal/index passes"
tmpdir="$(mktemp -d "${ROOT_DIR}/tests/design_package/019-internal-index-task-1-use-body-hash-for-staleness-in-freshness/.fieldcheck.XXXXXX")"
trap 'rm -rf "${tmpdir}"' EXIT
cat >"${tmpdir}/source_record_field_test.go" <<'GOEOF'
package source_record_field_test

import (
	"reflect"
	"testing"

	"gitcode-mcp/internal/index"
)

func TestSourceRecordPreviousIndexedHashRemoved(t *testing.T) {
	if _, ok := reflect.TypeOf(index.SourceRecord{}).FieldByName("PreviousIndexedHash"); ok {
		t.Fatalf("SourceRecord still exposes removed PreviousIndexedHash field")
	}
}
GOEOF
go test "${tmpdir}" -count=1
go test ./internal/index/ -count=1
