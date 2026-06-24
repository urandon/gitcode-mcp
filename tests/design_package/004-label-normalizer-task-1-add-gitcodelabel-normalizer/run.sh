#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
FAILURES=0

echo "=== Validating 004-label-normalizer-task-1-add-gitcodelabel-normalizer ==="

# --- Scenario 1: Labels encoded as JSON string in create request ---
echo "[SCENARIO-1] create-issue request body contains labels as JSON string"
cd "$ROOT_DIR"
if go test -run "^TestLabel011CreateRequestLabelString$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: Scenario 1 - labels serialized as JSON string in create request"
else
    echo "  FAIL: Scenario 1 - labels serialization check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Scenario 2: Live issue sync normalizes label objects to []string ---
echo "[SCENARIO-2] issue response label objects normalize to cache Labels []string"
cd "$ROOT_DIR"
if go test -run "^TestLabel010IssueResponseObjects$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: Scenario 2 - label objects normalized to []string{\"bug\"}"
else
    echo "  FAIL: Scenario 2 - label object normalization failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Scenario 3a: Missing name returns schema_decode ---
echo "[SCENARIO-3a] label with missing name returns schema_decode diagnostic"
cd "$ROOT_DIR"
if go test -run "^TestLabel007NormalizeMissingName$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: Scenario 3a - missing name produces ErrSchemaDecode"
else
    echo "  FAIL: Scenario 3a - missing name diagnostic check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Scenario 3b: Missing id returns schema_decode ---
echo "[SCENARIO-3b] label with missing id returns schema_decode diagnostic"
cd "$ROOT_DIR"
if go test -run "^TestLabel015ObjectLabelWithMissingIDReturnsSchemaDecode$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: Scenario 3b - missing id produces ErrSchemaDecode"
else
    echo "  FAIL: Scenario 3b - missing id diagnostic check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Scenario 3c: schema_decode distinct from transport ---
echo "[SCENARIO-3c] schema_decode is distinct from transport failure"
cd "$ROOT_DIR"
if go test -run "^TestLabel014SchemaDecodeDistinctFromTransport$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: Scenario 3c - schema_decode distinct from ErrNetworkUnavailable"
else
    echo "  FAIL: Scenario 3c - schema_decode vs transport distinctiveness check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Scenario 4: Empty labels produces "[]" ---
echo "[SCENARIO-4] create-issue with empty labels produces \"[]\""
cd "$ROOT_DIR"
if go test -run "^TestLabel012CreateRequestEmptyLabels$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: Scenario 4 - empty labels encoded as \"[]\""
else
    echo "  FAIL: Scenario 4 - empty labels encoding check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Scenario 5: JSON array labels rejected ---
echo "[SCENARIO-5] JSON array labels cause test failure"
cd "$ROOT_DIR"
if go test -run "^TestLabel013ArrayLabelsRejected$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: Scenario 5 - array labels rejected with 400"
else
    echo "  FAIL: Scenario 5 - array labels rejection check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Scenario 6: go test ./... and git diff --check pass ---
echo "[SCENARIO-6] go test ./... and git diff --check"
cd "$ROOT_DIR"
if go test ./... -count=1 >/dev/null 2>&1; then
    echo "  PASS: Scenario 6a - go test ./... passes"
else
    echo "  FAIL: Scenario 6a - go test ./... failed"
    FAILURES=$((FAILURES + 1))
fi
if git diff --check >/dev/null 2>&1; then
    echo "  PASS: Scenario 6b - git diff --check passes"
else
    echo "  FAIL: Scenario 6b - git diff --check failed"
    FAILURES=$((FAILURES + 1))
fi

echo ""
if [ "$FAILURES" -eq 0 ]; then
    echo "=== ALL SCENARIOS PASSED ==="
    exit 0
else
    echo "=== $FAILURES SCENARIO(S) FAILED ==="
    exit 1
fi
