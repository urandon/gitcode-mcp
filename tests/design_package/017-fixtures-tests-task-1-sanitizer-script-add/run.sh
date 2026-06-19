#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
harness_dir="$repo_root/tests/design_package/017-fixtures-tests-task-1-sanitizer-script-add"
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/gitcode-mcp-sanitizer-validation.XXXXXX")
fixture_backup=""
fixture_installed=0

cleanup() {
  if [[ $fixture_installed -eq 1 ]]; then
    rm -rf "$repo_root/fixtures"
    if [[ -n "$fixture_backup" && -e "$fixture_backup" ]]; then
      mv "$fixture_backup" "$repo_root/fixtures"
    fi
  elif [[ -n "$fixture_backup" && -e "$fixture_backup" ]]; then
    rm -rf "$repo_root/fixtures"
    mv "$fixture_backup" "$repo_root/fixtures"
  fi
  rm -rf "$work_dir"
}
trap cleanup EXIT

python3 - "$harness_dir" <<'PY'
import json
import pathlib
import sys

harness = pathlib.Path(sys.argv[1])
scenarios = (harness / "scenarios.md").read_text(encoding="utf-8")
required_ids = [
    "017-fixtures-tests-task-1-sanitizer-script-add-scenario-1",
    "017-fixtures-tests-task-1-sanitizer-script-add-scenario-2",
    "017-fixtures-tests-task-1-sanitizer-script-add-scenario-3",
]
missing = [scenario_id for scenario_id in required_ids if scenario_id not in scenarios]
if missing:
    raise SystemExit(f"scenarios.md missing required ids: {missing}")
validation = json.loads((harness / "validation.json").read_text(encoding="utf-8"))
for key in [
    "covered_outcome_ids",
    "covered_decommission_ids",
    "product_surfaces",
    "evidence_type",
    "freshness",
    "mocks_used",
    "production_files_modified",
]:
    if key not in validation:
        raise SystemExit(f"validation.json missing required key: {key}")
if validation["covered_outcome_ids"] != ["outcome-7"]:
    raise SystemExit("validation.json must cover outcome-7 only")
if validation["covered_decommission_ids"] != []:
    raise SystemExit("validation.json must not list decommission coverage")
if validation["production_files_modified"] != []:
    raise SystemExit("validation.json must declare no production files modified")
PY

raw_dir="$work_dir/raw"
out_dir="$work_dir/sanitized-fixtures"
mkdir -p "$raw_dir/captures/api/v5/repos/raw-owner/raw-repo/issues/42"
mkdir -p "$raw_dir/transcripts/gitcode.example.invalid/api/v5/repos/raw-owner/raw-repo/issues/42"

cat > "$raw_dir/captures/api/v5/repos/raw-owner/raw-repo/issues/42/response.json" <<'JSON'
{
  "id": 42,
  "owner": "raw-owner",
  "repo": "raw-repo",
  "project": "raw-project",
  "url": "https://gitcode.example.invalid/api/v5/repos/raw-owner/raw-repo/issues/42",
  "html_url": "https://gitcode.example.invalid/raw-owner/raw-repo/raw-project",
  "nested": {
    "Authorization": "Bearer raw-secret-token",
    "api_host": "gitcode.example.invalid"
  }
}
JSON

cat > "$raw_dir/transcripts/gitcode.example.invalid/api/v5/repos/raw-owner/raw-repo/issues/42/transcript.http" <<'HTTP'
GET /api/v5/repos/raw-owner/raw-repo/issues/42 HTTP/1.1
Host: gitcode.example.invalid
Authorization: Bearer raw-secret-token

HTTP/1.1 200 OK
Content-Type: application/json

{"html_url":"https://gitcode.example.invalid/raw-owner/raw-repo/raw-project"}
HTTP

cat > "$raw_dir/captures/api/v5/repos/raw-owner/raw-repo/issues/42/body.txt" <<'TXT'
Issue raw-project belongs to raw-owner/raw-repo at https://gitcode.example.invalid/api/v5/repos/raw-owner/raw-repo/issues/42.
TXT

(
  cd "$repo_root"
  scripts/sanitize-fixtures.sh "$raw_dir" "$out_dir" --owner raw-owner --repo raw-repo --project raw-project --host gitcode.example.invalid
)

expected_json="$out_dir/api/v5/repos/example-owner/example-repo/issues/42/response.json"
expected_http="$out_dir/api/v5/repos/example-owner/example-repo/issues/42/transcript.http"
expected_text="$out_dir/api/v5/repos/example-owner/example-repo/issues/42/body.txt"
[[ -f "$expected_json" ]] || { printf 'missing sanitized endpoint-shaped JSON path: %s\n' "$expected_json" >&2; exit 1; }
[[ -f "$expected_http" ]] || { printf 'missing sanitized endpoint-shaped transcript path: %s\n' "$expected_http" >&2; exit 1; }
[[ -f "$expected_text" ]] || { printf 'missing sanitized endpoint-shaped text path: %s\n' "$expected_text" >&2; exit 1; }

python3 - "$out_dir" <<'PY'
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
required = {"api.example.com", "example-owner", "example-repo", "example-project"}
forbidden = [
    "Authorization",
    "raw-owner",
    "raw-repo",
    "raw-project",
    "gitcode.example.invalid",
    "raw-secret-token",
]
combined = []
for path in sorted(p for p in root.rglob("*") if p.is_file()):
    rel = path.relative_to(root).as_posix()
    text = path.read_text(encoding="utf-8")
    combined.append(rel + "\n" + text)
    for token in forbidden:
        if token in rel or token in text:
            raise SystemExit(f"forbidden token {token!r} survived in {rel}")
    for host in re.findall(r"(?i)\b(?:[a-z0-9-]+\.)+[a-z]{2,}\b", text):
        if host != "api.example.com":
            raise SystemExit(f"disallowed hostname {host!r} survived in {rel}")
all_text = "\n".join(combined)
missing = sorted(token for token in required if token not in all_text)
if missing:
    raise SystemExit(f"missing placeholder tokens: {missing}")
PY

if [[ -e "$repo_root/fixtures" ]]; then
  fixture_backup="$work_dir/fixtures.backup"
  mv "$repo_root/fixtures" "$fixture_backup"
fi
cp -R "$out_dir" "$repo_root/fixtures"
fixture_installed=1

(
  cd "$repo_root"
  go test ./internal/gitcode/... -run TestSanitizedFixtures -count=1
  go test ./... -count=1
)

rm -rf "$repo_root/fixtures"
if [[ -n "$fixture_backup" && -e "$fixture_backup" ]]; then
  mv "$fixture_backup" "$repo_root/fixtures"
fi
fixture_installed=0

(
  cd "$repo_root"
  git diff --check
)

printf 'sanitizer validation passed\n'
