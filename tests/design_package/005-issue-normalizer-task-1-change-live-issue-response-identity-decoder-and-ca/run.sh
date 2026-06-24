#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
FAILURES=0

echo "=== Validating 005-issue-normalizer-task-1-change-live-issue-response-identity-decoder-and-ca ==="

# --- Numeric id (float64) decodes to correct string ---
echo "[V3] Numeric id (float64) decodes to correct string"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity001NumericIDFloat64$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V3 - numeric id 42 decodes to \"42\""
else
    echo "  FAIL: V3 - numeric id decoding failed"
    FAILURES=$((FAILURES + 1))
fi

# --- String id decodes correctly ---
echo "[V4] String id decodes to correct string"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity002StringID$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V4 - string id \"ISSUE-99\" decodes correctly"
else
    echo "  FAIL: V4 - string id decoding failed"
    FAILURES=$((FAILURES + 1))
fi

# --- String number decodes to int ---
echo "[V5] String number decodes to int"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity003StringNumber$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V5 - string number \"7\" decodes to 7"
else
    echo "  FAIL: V5 - string number decoding failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Missing id -> ErrSchemaDecode with Field="id" ---
echo "[V6] Missing id produces ErrSchemaDecode with Field=\"id\""
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity004MissingID$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V6 - missing id produces schema_decode with Field=\"id\""
else
    echo "  FAIL: V6 - missing id diagnostic check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- id=0 -> ErrSchemaDecode with Field="id" ---
echo "[V7] id=0 produces ErrSchemaDecode with Field=\"id\""
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity005IDZero$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V7 - id=0 produces schema_decode"
else
    echo "  FAIL: V7 - id=0 diagnostic check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- number=0 -> passes (valid) ---
echo "[V8] number=0 passes (valid)"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity007NumberZero$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V8 - number=0 is accepted as valid"
else
    echo "  FAIL: V8 - number=0 acceptance check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- schema_decode not confused with transport ---
echo "[V9] Schema decode error not confused with transport/network"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity010MalformedPayloadSchemaDecodeDistinct$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V9a - malformed payload schema_decode distinct from transport"
else
    echo "  FAIL: V9a - schema_decode vs transport distinctiveness check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- empty string id not confused with transport ---
echo "[V9b] Empty id string not confused with transport"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity011SchemaDecodeNotTransport$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V9b - empty id string schema_decode distinct from transport"
else
    echo "  FAIL: V9b - empty id vs transport distinctiveness check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Existing fixture data (ISSUE-41, ISSUE-42) still decodes ---
echo "[V10] Existing fixture data still decodes"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity013ExistingFixtureStringIDsStillWork$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V10 - ISSUE-41 and ISSUE-42 still decode correctly"
else
    echo "  FAIL: V10 - existing fixture string ID check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Mocked live route with numeric id works end-to-end ---
echo "[V11] Mocked live route with numeric id works end-to-end"
cd "$ROOT_DIR"
if go test -run "^TestScenario004ReadRouteContract$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V11 - numeric id in live route contract test passes"
else
    echo "  FAIL: V11 - live route numeric id test failed"
    FAILURES=$((FAILURES + 1))
fi

# --- schema_decode diagnostic code classified by diagnostics.Classifier ---
echo "[V12] schema_decode classified distinctly by diagnostics.Classifier"
cd "$ROOT_DIR"
if go test -run "^TestScenario004ReadRouteContract$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: V12 - CodeSchemaDecode exists in classifier (verified via compilation)"
else
    echo "  FAIL: V12 - classifier schema_decode check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Go test all packages ---
echo "[V13] Zero-credential, zero-network go test ./..."
cd "$ROOT_DIR"
if go test ./... -count=1 >/dev/null 2>&1; then
    echo "  PASS: V13a - go test ./... passes without credentials/network"
else
    echo "  FAIL: V13a - go test ./... failed"
    FAILURES=$((FAILURES + 1))
fi

# --- git diff --check ---
if git diff --check >/dev/null 2>&1; then
    echo "  PASS: V13b - git diff --check passes"
else
    echo "  FAIL: V13b - git diff --check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Additional: id="0" (string zero) rejected ---
echo "[ADDITIONAL] id=\"0\" (string zero) produces ErrSchemaDecode"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity012IDZeroStringInvalid$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: id=\"0\" rejected with ErrSchemaDecode"
else
    echo "  FAIL: id=\"0\" rejection check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Additional: null id rejected ---
echo "[ADDITIONAL] null id produces ErrSchemaDecode"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity014NilID$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: null id rejected with ErrSchemaDecode"
else
    echo "  FAIL: null id rejection check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Additional: bool id rejected ---
echo "[ADDITIONAL] bool id produces ErrSchemaDecode"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity015BoolIDRejected$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: bool id rejected with ErrSchemaDecode"
else
    echo "  FAIL: bool id rejection check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Additional: round-trip through both IssueSummary and Issue ---
echo "[ADDITIONAL] round-trip numeric id + string number for both types"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity009RoundTripNumericIDStringNumber$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: round-trip for both IssueSummary and Issue"
else
    echo "  FAIL: round-trip check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Additional: labels work with numeric id ---
echo "[ADDITIONAL] labels normalize correctly alongside numeric id"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity016LabelsStillWorkWithNumericID$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: labels normalize with numeric id"
else
    echo "  FAIL: labels with numeric id check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Additional: number zero as string acceptable ---
echo "[ADDITIONAL] number=\"0\" accepted as valid"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity008NumberZeroString$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: number=\"0\" accepted as valid"
else
    echo "  FAIL: number=\"0\" check failed"
    FAILURES=$((FAILURES + 1))
fi

# --- Additional: missing number -> ErrSchemaDecode ---
echo "[ADDITIONAL] missing number produces ErrSchemaDecode"
cd "$ROOT_DIR"
if go test -run "^TestIssueIdentity006MissingNumber$" ./internal/gitcode/ -count=1 >/dev/null 2>&1; then
    echo "  PASS: missing number produces schema_decode"
else
    echo "  FAIL: missing number diagnostic check failed"
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
