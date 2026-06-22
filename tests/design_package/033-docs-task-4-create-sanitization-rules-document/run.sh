#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

require_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  [[ "$haystack" == *"$needle"* ]] || fail "$label missing expected text: $needle"
}

require_not_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  [[ "$haystack" != *"$needle"* ]] || fail "$label leaked forbidden text: $needle"
}

require_file_contains() {
  local file="$1"
  local needle="$2"
  [[ -f "$file" ]] || fail "missing file: $file"
  python3 - "$file" "$needle" <<'PY' || exit 1
from pathlib import Path
import sys
path = Path(sys.argv[1])
needle = sys.argv[2]
if needle not in path.read_text():
    print(f"FAIL: {path} missing expected text: {needle}", file=sys.stderr)
    sys.exit(1)
PY
}

require_section_order() {
  python3 - <<'PY' || exit 1
from pathlib import Path
import sys
text = Path('docs/sanitization.md').read_text()
sections = [
    '## 1. Purpose',
    '## 2. Redacted Surface Types',
    '## 3. Safe Replacement Patterns',
    '## 4. Surface-Specific Rules',
    '## 5. Verification',
]
pos = -1
for section in sections:
    idx = text.find(section)
    if idx <= pos:
        print(f'FAIL: missing or out-of-order section: {section}', file=sys.stderr)
        sys.exit(1)
    pos = idx
PY
}

printf 'SCN-DOCS-SANITIZATION-EXISTS\n'
[[ -f docs/sanitization.md ]] || fail 'docs/sanitization.md does not exist'
require_section_order
require_file_contains docs/sanitization.md 'Token value (from `GITCODE_TOKEN` env or keychain)'
require_file_contains docs/sanitization.md 'Private repo coordinates in JSON'
require_file_contains docs/sanitization.md 'Private repo coordinates in text'
require_file_contains docs/sanitization.md '### CLI output'
require_file_contains docs/sanitization.md '### MCP tool responses'
require_file_contains docs/sanitization.md '### E2e test output'
require_file_contains docs/sanitization.md '### Fixture files'
require_file_contains docs/sanitization.md '### Logs and diagnostics'

printf 'SCN-DOCS-SANITIZATION-README-LINK\n'
require_file_contains README.md 'docs/sanitization.md'

printf 'SCN-DOCS-SANITIZATION-PLACEHOLDERS\n'
for placeholder in 'YOUR_OWNER' 'YOUR_REPO' '$GITCODE_TOKEN' '[REDACTED]'; do
  require_file_contains docs/sanitization.md "$placeholder"
  require_file_contains docs/live-readiness.md "$placeholder"
done
python3 - <<'PY' || exit 1
from pathlib import Path
import re, sys
for path in [Path('docs/sanitization.md'), Path('docs/live-readiness.md')]:
    text = path.read_text()
    forbidden = [
        r'glpat-[A-Za-z0-9_-]{8,}',
        r'GITCODE-PAT-[A-Za-z0-9_-]+',
        r'Authorization:\s*Bearer\s+(?!\[REDACTED\])\S+',
    ]
    for pattern in forbidden:
        if re.search(pattern, text, re.I):
            print(f'FAIL: {path} contains forbidden pattern {pattern}', file=sys.stderr)
            sys.exit(1)
PY

printf 'SCN-DOCS-SANITIZATION-TOKEN-REDACTION\n'
TOKEN='glpat-validation-token-123456'
TMPDIR_VALIDATION="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_VALIDATION"' EXIT
AUTH_OUTPUT="$({ GITCODE_MCP_CACHE_DIR="$TMPDIR_VALIDATION/cache" GITCODE_MCP_CONFIG_DIR="$TMPDIR_VALIDATION/config" GITCODE_TOKEN="$TOKEN" go run ./cmd/gitcode-mcp auth status; } 2>&1)" || fail "auth status failed: $AUTH_OUTPUT"
require_contains "$AUTH_OUTPUT" 'token_present: true' 'auth status output'
require_contains "$AUTH_OUTPUT" 'redacted_token:' 'auth status output'
require_not_contains "$AUTH_OUTPUT" "$TOKEN" 'auth status output'
if [[ "$AUTH_OUTPUT" != *'redacted_token: [REDACTED]'* && ! "$AUTH_OUTPUT" =~ redacted_token:[[:space:]]+[A-Za-z0-9_-]{3}\*\*\*[A-Za-z0-9_-]{3} ]]; then
  fail "auth status token redaction did not match [REDACTED] or preview format: $AUTH_OUTPUT"
fi
require_file_contains docs/sanitization.md 'first 3 characters + `***` + last 3 characters'
require_file_contains docs/sanitization.md '[REDACTED]'

printf 'SCN-DOCS-SANITIZATION-SURFACES\n'
CACHE_PATH="$TMPDIR_VALIDATION/cache.db"
OWNER='YOUR_OWNER'
REPO='YOUR_REPO'
ADD_OUTPUT="$({ go run ./cmd/gitcode-mcp repo add --repo validation-repo --owner "$OWNER" --name "$REPO" --api-base-url 'https://gitcode.example.invalid/api/v5' --scopes 'issues,wiki' --cache-path "$CACHE_PATH"; } 2>&1)" || fail "repo add failed: $ADD_OUTPUT"
DOCTOR_OUTPUT="$({ GITCODE_TOKEN="$TOKEN" go run ./cmd/gitcode-mcp doctor --repo validation-repo --cache-path "$CACHE_PATH"; } 2>&1)" || fail "doctor failed: $DOCTOR_OUTPUT"
require_contains "$DOCTOR_OUTPUT" 'owner: [REDACTED]' 'doctor output'
require_contains "$DOCTOR_OUTPUT" 'name: [REDACTED]' 'doctor output'
require_not_contains "$DOCTOR_OUTPUT" "$OWNER" 'doctor output'
require_not_contains "$DOCTOR_OUTPUT" "$REPO" 'doctor output'
require_not_contains "$DOCTOR_OUTPUT" "$TOKEN" 'doctor output'
require_file_contains docs/sanitization.md 'The `doctor` command redacts owner/repo'
require_file_contains docs/sanitization.md 'JSON response bodies from MCP tools are sanitized'
require_file_contains docs/sanitization.md 'Fixture files under `internal/` and `project/` must contain no real tokens'

python3 - <<'PY' || exit 1
from pathlib import Path
import re, sys
patterns = [
    re.compile(r'glpat-[A-Za-z0-9_-]{12,}', re.I),
    re.compile(r'GITCODE-PAT-[A-Za-z0-9_-]+', re.I),
    re.compile(r'Authorization:\s*Bearer\s+(?!\[REDACTED\])\S+', re.I),
    re.compile(r'Cookie:\s*(?!\[REDACTED\])\S+', re.I),
    re.compile(r'Set-Cookie:\s*(?!\[REDACTED\])\S+', re.I),
]
roots = [Path('internal'), Path('project')]
skip_parts = {'tests', 'testdata'}
allow_files = {
    Path('internal/diagnostics/redaction_test.go'),
    Path('internal/gitcode/sanitized_fixtures_test.go'),
    Path('project/dogfood/validate-fixtures.sh'),
}
for root in roots:
    if not root.exists():
        continue
    for path in root.rglob('*'):
        if not path.is_file():
            continue
        rel = path.relative_to(Path('.'))
        if rel in allow_files:
            continue
        name = path.name.lower()
        rels = str(rel).lower()
        fixture_like = 'fixture' in name or 'fixture' in rels or 'testdata' in rels or path.suffix in {'.json', '.yaml', '.yml'}
        if not fixture_like:
            continue
        try:
            text = path.read_text(errors='ignore')
        except Exception:
            continue
        for pattern in patterns:
            if pattern.search(text):
                print(f'FAIL: fixture-like file {rel} contains forbidden pattern {pattern.pattern}', file=sys.stderr)
                sys.exit(1)
PY

printf 'PASS: sanitization validation scenarios passed\n'
