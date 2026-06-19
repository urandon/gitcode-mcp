#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
HARNESS_DIR="$ROOT_DIR/tests/design_package/005-agent-query-surface-task-1-add-service-query-api"
cd "$ROOT_DIR"

export GONOSUMDB="${GONOSUMDB:-*}"
unset GITCODE_TEST_TOKEN GITCODE_TOKEN GITCODE_BASE_URL GITCODE_HOST

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
    "005-agent-query-surface-task-1-add-service-query-api-scenario-1",
    "005-agent-query-surface-task-1-add-service-query-api-scenario-2",
    "005-agent-query-surface-task-1-add-service-query-api-scenario-3",
    "005-agent-query-surface-task-1-add-service-query-api-scenario-4",
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
required_commands = [
    "go test ./internal/service/... -run 'Test(SearchSources|GetSource|ListSources|GetBacklinks|ResolveID|GetSnippet|GetSyncStatus|RecentChanges|LinkCheck|StaleIndex|ExportSnapshot|DiffSnapshot)' -count=1",
    "go test ./internal/service/... -run TestMCPToolDTOContract -count=1",
    "go test ./internal/service/... -run TestQueryEdgeCases -count=1",
    "go test ./internal/service/... -run TestQueryMethodsDoNotUseLiveNetwork -count=1",
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

if validation["covered_outcome_ids"] != ["outcome-4", "outcome-5", "outcome-10"]:
    raise SystemExit("validation.json must cover outcome-4, outcome-5, and outcome-10 for this task")
if validation["covered_decommission_ids"] != ["decommission-1", "decommission-3"]:
    raise SystemExit("validation.json must cover decommission-1 and decommission-3 for this task")
if validation["production_files_modified"] != []:
    raise SystemExit("validation harness must not declare production file modifications")
for command in required_commands:
    if command not in run_script:
        raise SystemExit(f"run.sh missing required product test command: {command}")
PY

printf '==> service query API scenario validation\n'
go test ./internal/service/... -run 'Test(SearchSources|GetSource|ListSources|GetBacklinks|ResolveID|GetSnippet|GetSyncStatus|RecentChanges|LinkCheck|StaleIndex|ExportSnapshot|DiffSnapshot)' -count=1

printf '==> MCP structuredContent DTO contract validation\n'
go test ./internal/service/... -run TestMCPToolDTOContract -count=1

printf '==> query edge case validation\n'
go test ./internal/service/... -run TestQueryEdgeCases -count=1

printf '==> cache-only no-live-network query validation\n'
go test ./internal/service/... -run TestQueryMethodsDoNotUseLiveNetwork -count=1

printf '==> service package regression validation\n'
go test ./internal/service/... -count=1

printf '==> repository compile and regression validation\n'
go test ./... -count=1

printf '==> whitespace validation\n'
git diff --check
