#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

export GITCODE_TEST_TOKEN=""
export GITCODE_TOKEN=""
export NO_COLOR=1

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT
CACHE_DB="$TMPDIR/cache.db"
JSON_ONE="$TMPDIR/export-one.json"
JSON_TWO="$TMPDIR/export-two.json"
SQLITE_OUT="$TMPDIR/export.sqlite"
FAILURES=0

run_check() {
  local name="$1"
  local command="$2"
  echo ""
  echo "=== $name ==="
  if bash -c "$command"; then
    echo "PASS: $name"
  else
    echo "FAIL: $name"
    FAILURES=$((FAILURES + 1))
  fi
}

run_check "scenario 1: deterministic service JSON and markdown exports" \
  "go test ./internal/service/... -run '^TestExportDeterminism$' -count=1"

run_check "scenario 2: service JSON includes chunk provenance" \
  "go test ./internal/service/... -run '^TestExportIncludesChunkProvenance$' -count=1"

run_check "scenario 3: service snapshot diff categories" \
  "go test ./internal/service/... -run '^TestDiffSnapshot$' -count=1"

run_check "scenario 4: CLI export coverage" \
  "go test ./internal/cli/... -run 'Test.*Export' -count=1"

run_check "scenario 4: CLI diff coverage" \
  "go test ./internal/cli/... -run 'Test.*Diff' -count=1"

run_check "scenario 4: offline runtime ingest/index/export deterministic JSON" \
  "go run ./cmd/gitcode-mcp ingest --cache-path '$CACHE_DB' >/dev/null && go run ./cmd/gitcode-mcp index --cache-path '$CACHE_DB' >/dev/null && go run ./cmd/gitcode-mcp export --format json --cache-path '$CACHE_DB' > '$JSON_ONE' && go run ./cmd/gitcode-mcp export --format json --cache-path '$CACHE_DB' > '$JSON_TWO' && cmp -s '$JSON_ONE' '$JSON_TWO' && python3 - '$JSON_ONE' <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
data = json.loads(path.read_text())
if 'inline_content' in data:
    snapshot = json.loads(data['inline_content'])
else:
    snapshot = data
if not snapshot.get('sources'):
    raise SystemExit('snapshot has no sources after offline ingest/index')
chunks = snapshot.get('chunks') or []
if not chunks:
    raise SystemExit('snapshot has no chunks after offline ingest/index')
required = {'id', 'source_id', 'byte_start', 'byte_end', 'line_start', 'line_end', 'heading_path', 'content_hash', 'inherited_metadata', 'outbound_links', 'resolved_aliases'}
missing = sorted(required - set(chunks[0]))
if missing:
    raise SystemExit(f'first chunk missing fields: {missing}')
text = path.read_text().lower()
for forbidden in ('legacy markdown index', 'markdown_index', 'source_file_read'):
    if forbidden in text:
        raise SystemExit(f'snapshot contains legacy/source-file sentinel: {forbidden}')
PY"

run_check "scenario 5: sqlite export conditional behavior" \
  "set +e; output=\$(go run ./cmd/gitcode-mcp export --format sqlite --cache-path '$CACHE_DB' --output '$SQLITE_OUT' 2>&1); code=\$?; set -e; if [ \$code -eq 0 ]; then [ -s '$SQLITE_OUT' ] && go test ./internal/service/... -run 'Test.*SQLite.*Snapshot|Test.*Snapshot.*SQLite' -count=1; else case \$output in *'format must be text, markdown, or json'*|*'format must be json or markdown'*|*unsupported*|*invalid*) exit 0 ;; *) printf '%s\n' \"\$output\" >&2; exit 1 ;; esac; fi"

run_check "validation artifact whitespace" \
  "git diff --check -- tests/design_package/016-rag-ready-corpus-task-2-add-corpus-snapshot-apis"

echo ""
if [ "$FAILURES" -eq 0 ]; then
  echo "ALL VALIDATIONS PASSED"
else
  echo "$FAILURES VALIDATION(S) FAILED"
  exit 1
fi
