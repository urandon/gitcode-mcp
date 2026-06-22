#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

CACHE_BOUND="$TMPDIR/bound.db"
CACHE_EMPTY="$TMPDIR/empty.db"
SECRET="doctor-validation-secret-token"

run_cli() {
  go run ./cmd/gitcode-mcp "$@"
}

run_cli repo add \
  --cache-path "$CACHE_BOUND" \
  --repo fixture-a \
  --owner owner-a \
  --name repo-a \
  --api-base-url https://example.invalid/api \
  --scopes issues,wiki \
  --format json >"$TMPDIR/repo-add.json"

GITCODE_TOKEN="$SECRET" go run ./cmd/gitcode-mcp doctor --cache-path "$CACHE_BOUND" --format json >"$TMPDIR/full-report.json"
env -u GITCODE_TOKEN go run ./cmd/gitcode-mcp doctor --cache-path "$CACHE_EMPTY" --format json >"$TMPDIR/no-binding-report.json"
env -u GITCODE_TOKEN go run ./cmd/gitcode-mcp doctor --cache-path "$TMPDIR/no-token.db" --format json >"$TMPDIR/no-token-report.json"

python3 - "$TMPDIR" "$SECRET" <<'PY'
import json
import pathlib
import sys

tmp = pathlib.Path(sys.argv[1])
secret = sys.argv[2]

full = json.loads((tmp / "full-report.json").read_text())
no_binding = json.loads((tmp / "no-binding-report.json").read_text())
no_token = json.loads((tmp / "no-token-report.json").read_text())
all_text = "\n".join(p.read_text() for p in tmp.glob("*.json"))

required_sections = [
    "version",
    "config",
    "cache",
    "repo",
    "credential",
    "live_provider",
    "auth_probe",
    "sync",
    "index",
    "mcp",
]
missing = [section for section in required_sections if section not in full]
if missing:
    raise SystemExit(f"full doctor report missing sections: {missing}")

if not str(full.get("version", "")).strip():
    raise SystemExit("full doctor report has empty version")
if full["cache"].get("status") != "available":
    raise SystemExit(f"cache status not available: {full['cache']}")
if not str(full["cache"].get("schema_version", "")).isdigit():
    raise SystemExit(f"schema version not surfaced: {full['cache']}")
if full["repo"].get("status") != "ready" or full["repo"].get("repo_id") != "fixture-a":
    raise SystemExit(f"repo binding not ready: {full['repo']}")
if full["credential"].get("status") != "token_configured":
    raise SystemExit(f"token status not configured: {full['credential']}")
if full["credential"].get("token_present") is not True:
    raise SystemExit(f"token_present not true: {full['credential']}")
if full["live_provider"].get("provider_mode") != "fixture":
    raise SystemExit(f"offline doctor did not report fixture provider mode: {full['live_provider']}")
if "status" not in full["auth_probe"] or "probe_result" not in full["auth_probe"]:
    raise SystemExit(f"auth probe fields missing: {full['auth_probe']}")
if full["sync"].get("status") != "available":
    raise SystemExit(f"sync status not available: {full['sync']}")
if full["index"].get("status") != "available":
    raise SystemExit(f"index status not available: {full['index']}")
if full["mcp"].get("transport_stdio") != "supported" or full["mcp"].get("transport_http") != "supported":
    raise SystemExit(f"MCP transport readiness missing: {full['mcp']}")

if no_binding["repo"].get("status") != "no_repo_bound":
    raise SystemExit(f"no-binding status missing: {no_binding['repo']}")
binding_text = json.dumps(no_binding, sort_keys=True).lower()
if "no repo bound" not in binding_text and "no_repo_bound" not in binding_text:
    raise SystemExit("no-binding report lacks no repo bound diagnostic")
if "repo add" not in binding_text:
    raise SystemExit("no-binding report lacks repo add bind suggestion")

no_token_text = json.dumps(no_token, sort_keys=True).lower()
if no_token["credential"].get("status") != "no_token_configured":
    raise SystemExit(f"no-token status missing: {no_token['credential']}")
if "no token configured" not in no_token_text and "no_token_configured" not in no_token_text:
    raise SystemExit("no-token report lacks no token configured diagnostic")
if not no_token["credential"].get("available_sources"):
    raise SystemExit(f"available credential sources missing: {no_token['credential']}")

for forbidden in [secret, "Authorization:", "Bearer " + secret, "access_token=", "cookie:"]:
    if forbidden.lower() in all_text.lower():
        raise SystemExit(f"public-safety failure: leaked forbidden text {forbidden!r}")
PY

printf 'doctor validation scenarios passed\n'
