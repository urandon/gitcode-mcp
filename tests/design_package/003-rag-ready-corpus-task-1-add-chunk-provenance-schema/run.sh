#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
HARNESS_DIR="$ROOT_DIR/tests/design_package/003-rag-ready-corpus-task-1-add-chunk-provenance-schema"
cd "$ROOT_DIR"

export GONOSUMDB="${GONOSUMDB:-*}"
unset GITCODE_TEST_TOKEN GITCODE_TOKEN

printf '==> validation contract check\n'
python3 - "$HARNESS_DIR" <<'PY'
import json
import pathlib
import sys

harness = pathlib.Path(sys.argv[1])
validation_path = harness / "validation.json"
scenarios_path = harness / "scenarios.md"
run_path = harness / "run.sh"

required_scenarios = [
    "003-rag-ready-corpus-task-1-add-chunk-provenance-schema-scenario-1",
    "003-rag-ready-corpus-task-1-add-chunk-provenance-schema-scenario-2",
    "003-rag-ready-corpus-task-1-add-chunk-provenance-schema-scenario-3",
]
required_fields = [
    "covered_outcome_ids",
    "covered_decommission_ids",
    "product_surfaces",
    "evidence_type",
    "freshness",
    "mocks_used",
    "production_files_modified",
]

validation = json.loads(validation_path.read_text())
scenarios = scenarios_path.read_text()
run_script = run_path.read_text()

missing_fields = [field for field in required_fields if field not in validation]
if missing_fields:
    raise SystemExit(f"validation.json missing required fields: {missing_fields}")

for scenario_id in required_scenarios:
    if scenario_id not in scenarios:
        raise SystemExit(f"scenarios.md missing required scenario id: {scenario_id}")
    if scenario_id not in validation.get("scenario_bindings", {}):
        raise SystemExit(f"validation.json missing scenario binding: {scenario_id}")

if validation["covered_outcome_ids"] != ["outcome-9"]:
    raise SystemExit("validation.json must cover only outcome-9 for this task")
if validation["covered_decommission_ids"] != []:
    raise SystemExit("validation.json must not cover decommission ids for this task")
if validation["production_files_modified"] != []:
    raise SystemExit("validation harness must not declare production file modifications")
if "go test ./internal/cache/... -run TestChunkSchemaEmbeddingColumn -count=1" not in run_script:
    raise SystemExit("run.sh missing cache schema product test command")
if "go test ./internal/index/... -run TestChunkDeterminism -count=1" not in run_script:
    raise SystemExit("run.sh missing index determinism product test command")
PY

printf '==> chunk provenance schema: embedding column validation\n'
go test ./internal/cache/... -run TestChunkSchemaEmbeddingColumn -count=1

printf '==> chunk identity validation\n'
go test ./internal/cache/... -run TestChunkIdentity -count=1

printf '==> chunk determinism and frontmatter invariant validation\n'
go test ./internal/index/... -run TestChunkDeterminism -count=1

printf '==> repository compile and regression validation\n'
go test ./... -count=1

printf '==> whitespace validation\n'
git diff --check
