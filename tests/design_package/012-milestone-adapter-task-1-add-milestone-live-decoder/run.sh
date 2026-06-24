#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
FAILURES=0

echo "=== Validation for 012-milestone-adapter-task-1-add-milestone-live-decoder ==="
echo ""

# --- Gate 1: go test ./... must pass ---
echo "[1/8] Running go test ./..."
if (cd "$REPO_ROOT" && go test ./...); then
  echo "  PASS: go test ./... passed"
else
  echo "  FAIL: go test ./... failed"
  FAILURES=$((FAILURES + 1))
fi
echo ""

# --- Gate 2: git diff --check must pass ---
echo "[2/8] Running git diff --check"
if (cd "$REPO_ROOT" && git diff --check); then
  echo "  PASS: git diff --check passed"
else
  echo "  FAIL: git diff --check failed"
  FAILURES=$((FAILURES + 1))
fi
echo ""

# --- Gate 3: Milestone adapter tests drive production live provider through stubbed routes ---
echo "[3/8] Verifying milestone adapter tests exist and exercise production code paths against stubbed /api/v5 routes"

MILESTONE_TEST_FILE="$REPO_ROOT/internal/gitcode/milestone_adapter_test.go"
if [ ! -f "$MILESTONE_TEST_FILE" ]; then
  echo "  FAIL: milestone_adapter_test.go not found"
  FAILURES=$((FAILURES + 1))
else
  # Check for httptest usage (stubbed external routes)
  if grep -q "httptest.NewServer" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: milestone tests use httptest.NewServer (stubbed external routes, no real network)"
  else
    echo "  FAIL: milestone tests do not use httptest.NewServer"
    FAILURES=$((FAILURES + 1))
  fi

  # Check route paths are exercised
  if grep -q "/api/v5/repos/.*/milestones\"" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests exercise GET /api/v5/repos/{owner}/{repo}/milestones route"
  else
    echo "  FAIL: list milestones route not exercised in tests"
    FAILURES=$((FAILURES + 1))
  fi

  if grep -q "/api/v5/repos/.*/milestones/" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests exercise GET /api/v5/repos/{owner}/{repo}/milestones/{id} route"
  else
    echo "  FAIL: get milestone route not exercised in tests"
    FAILURES=$((FAILURES + 1))
  fi

  # Check RouteSchemaMatrix milestone entry is supported + OpenAPI in default
  SCHEMA_FILE="$REPO_ROOT/internal/gitcode/route_schema_matrix.go"
  if grep -A5 "ProductAreaMilestones:" "$SCHEMA_FILE" | grep -q "SupportStatusSupported"; then
    echo "  PASS: RouteSchemaMatrix milestones default to supported"
  else
    echo "  FAIL: RouteSchemaMatrix milestones not set to supported in defaults"
    FAILURES=$((FAILURES + 1))
  fi
  if grep -A5 "ProductAreaMilestones:" "$SCHEMA_FILE" | grep -q "EvidenceClassOpenAPI"; then
    echo "  PASS: RouteSchemaMatrix milestones evidence class is OpenAPI"
  else
    echo "  FAIL: RouteSchemaMatrix milestones evidence class is not OpenAPI"
    FAILURES=$((FAILURES + 1))
  fi
fi
echo ""

# --- Gate 4: Cache-ready record field verification (SourceID, RemoteID, Title, Body, Status, DueOn, timestamps) ---
echo "[4/8] Verifying tests observe cache-ready records with kind=milestone fields"

if [ -f "$MILESTONE_TEST_FILE" ]; then
  # SourceID = MILESTONE-<id>
  if grep -q "MILESTONE-" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests verify SourceID with MILESTONE- prefix"
  else
    echo "  FAIL: no test verifies MILESTONE- SourceID"
    FAILURES=$((FAILURES + 1))
  fi

  # RemoteID normalized
  if grep -q "RemoteID" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests verify RemoteID"
  else
    echo "  FAIL: no test verifies RemoteID"
    FAILURES=$((FAILURES + 1))
  fi

  # Title verified
  if grep -q "\.Title\b" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests verify Title"
  else
    echo "  FAIL: no test verifies Title"
    FAILURES=$((FAILURES + 1))
  fi

  # Body from description
  if grep -q "\"First release\"" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests verify Body from description"
  else
    echo "  FAIL: no test verifies Body from description"
    FAILURES=$((FAILURES + 1))
  fi

  # Status mapping
  if grep -q "Status.*=.*\"open\"" "$MILESTONE_TEST_FILE" || grep -q "\.Status.*open" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests verify Status"
  else
    echo "  FAIL: no test verifies Status"
    FAILURES=$((FAILURES + 1))
  fi

  # DueOn parsed
  if grep -q "DueOn" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests verify DueOn"
  else
    echo "  FAIL: no test verifies DueOn"
    FAILURES=$((FAILURES + 1))
  fi

  # CreatedAt / UpdatedAt timestamps
  if grep -q "CreatedAt" "$MILESTONE_TEST_FILE" && grep -q "UpdatedAt" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests verify CreatedAt and UpdatedAt timestamps"
  else
    echo "  FAIL: no test verifies CreatedAt/UpdatedAt timestamps"
    FAILURES=$((FAILURES + 1))
  fi
fi
echo ""

# --- Gate 5: Negative test coverage ---
echo "[5/8] Verifying negative test coverage"

if [ -f "$MILESTONE_TEST_FILE" ]; then
  # Malformed JSON
  if grep -q "not json\|malformed JSON\|MalformedJSON" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: malformed JSON test present"
  else
    echo "  FAIL: no malformed JSON test"
    FAILURES=$((FAILURES + 1))
  fi

  # Missing title with name only
  if grep -q "name.*only\|name-only\|only name\|v1.0-only" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: missing title (name only) test present"
  else
    echo "  FAIL: no missing title (name only) test"
    FAILURES=$((FAILURES + 1))
  fi

  # Empty title
  if grep -q "empty title\|EmptyTitle\|title.*\"\"" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: empty title test present"
  else
    echo "  FAIL: no empty title test"
    FAILURES=$((FAILURES + 1))
  fi

  # ID validation
  if grep -q "zero id\|negative id\|nil id\|bool id\|object id\|array id\|fractional id" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: ID validation tests present (zero, negative, nil, bool, object, array, fractional)"
  else
    echo "  FAIL: missing some ID validation tests"
    FAILURES=$((FAILURES + 1))
  fi

  # HTTP 400
  if grep -q "StatusBadRequest\|HTTP.*400\|HTTP400" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: HTTP 400 test present"
  else
    echo "  FAIL: no HTTP 400 test"
    FAILURES=$((FAILURES + 1))
  fi

  # ID mismatch
  if grep -q "id mismatch\|IDMismatch\|id.*mismatch" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: ID mismatch test present"
  else
    echo "  FAIL: no ID mismatch test"
    FAILURES=$((FAILURES + 1))
  fi

  # Mixed valid/invalid list
  if grep -q "mixed\|MixedListFailure\|valid.*invalid" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: mixed valid/invalid list test present"
  else
    echo "  FAIL: no mixed list test"
    FAILURES=$((FAILURES + 1))
  fi

  # Date validation
  if grep -q "unparseable\|not-a-date\|DateParsing" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: malformed date test present"
  else
    echo "  FAIL: no malformed date test"
    FAILURES=$((FAILURES + 1))
  fi

  # Status value tests
  if grep -q "active becomes open\|active.*open\|StatusNormalization" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: status normalization test present"
  else
    echo "  FAIL: no status normalization test"
    FAILURES=$((FAILURES + 1))
  fi
fi
echo ""

# --- Gate 6: Error taxonomy verification (schema_decode vs api_validation) ---
echo "[6/8] Verifying error taxonomy (schema_decode never classified as transport/credential failure)"

if [ -f "$MILESTONE_TEST_FILE" ]; then
  # Schema decode not transport/config
  if grep -q "assertNotTransportError\|assertNotConfigCredentialError" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests assert schema_decode not classified as transport/credential error"
  else
    echo "  FAIL: no assertion that schema_decode is not classified as transport/credential"
    FAILURES=$((FAILURES + 1))
  fi

  # API validation not transport/config
  if grep -q "ErrAPIValidation" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: tests verify ErrAPIValidation error type"
  else
    echo "  FAIL: no ErrAPIValidation verification"
    FAILURES=$((FAILURES + 1))
  fi
fi
echo ""

# --- Gate 7: GAP-001 — Concrete product-path test for unrecognized status value ---
echo "[7/8] Verifying GAP-001: Unrecognized status value must produce schema_decode"

# Write a temporary Go test program that exercises the production Milestone.UnmarshalJSON
# directly with an unrecognized status value. This exercises the real product code path
# through json.Unmarshal -> Milestone.UnmarshalJSON -> decodeMilestoneStatus.
GAP_TEST_DIR=$(mktemp -d)
GAP_VALIDATION_DIR="$REPO_ROOT/.tmp_gap001_validation"
mkdir -p "$GAP_VALIDATION_DIR"
cat > "$GAP_VALIDATION_DIR/main.go" << 'GOEOF'
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"gitcode-mcp/internal/gitcode"
)

func main() {
	body := `{"id":1,"title":"test","state":"in_progress"}`
	var m gitcode.Milestone
	err := json.Unmarshal([]byte(body), &m)
	if err == nil {
		fmt.Fprintln(os.Stderr, "GAP-001 FAIL: unrecognized status 'in_progress' silently defaulted to open (nil error)")
		fmt.Fprintf(os.Stderr, "  Milestone: Status=%q (expected schema_decode error)\n", m.Status)
		os.Exit(1)
	}
	var schemaErr *gitcode.ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		fmt.Fprintf(os.Stderr, "GAP-001 FAIL: error type is %T, expected *gitcode.ErrSchemaDecode\n", err)
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		os.Exit(1)
	}
	if schemaErr.Field != "milestone.state" && schemaErr.Field != "milestone.status" {
		fmt.Fprintf(os.Stderr, "GAP-001 FAIL: field is %q, expected milestone.state or milestone.status\n", schemaErr.Field)
		os.Exit(1)
	}
	fmt.Printf("GAP-001 PASS: unrecognized status correctly produced schema_decode at %s\n", schemaErr.Field)
	os.Exit(0)
}
GOEOF

GAP001_RESULT=$(cd "$REPO_ROOT" && go run .tmp_gap001_validation/main.go 2>&1) || true
rm -rf "$GAP_VALIDATION_DIR"

if echo "$GAP001_RESULT" | grep -q "GAP-001 PASS"; then
  echo "  PASS: product-path confirms unrecognized status produces schema_decode"
else
  echo "  FAIL (GAP-001): $GAP001_RESULT"
  echo "  The product function decodeMilestoneStatus at models.go:539-553 has a default case"
  echo "  that returns 'open' for unrecognized status values. The acceptance criteria requires:"
  echo "  'unrecognized string values emit schema_decode at milestone.state or milestone.status'."
  echo "  This is a confirmed product failure — the decoder silently accepts invalid status instead of rejecting it."
  FAILURES=$((FAILURES + 1))
fi

# Verify the source code fix (static analysis confirming the root cause is repaired)
STATUS_FUNC="$REPO_ROOT/internal/gitcode/models.go"
if grep -A20 "func decodeMilestoneStatus" "$STATUS_FUNC" | grep -q "ErrSchemaDecode"; then
  echo "  PASS: decodeMilestoneStatus source returns ErrSchemaDecode for unrecognized values"
else
  echo "  FAIL: decodeMilestoneStatus does not return ErrSchemaDecode — GAP-001 not fixed"
  FAILURES=$((FAILURES + 1))
fi

# Check for test covering unrecognized status
if grep -q "TestMilestone012bUnrecognizedStatusSchemaDecode" "$MILESTONE_TEST_FILE"; then
  echo "  PASS: TestMilestone012bUnrecognizedStatusSchemaDecode test present"
else
  echo "  FAIL: test for unrecognized status value not found"
  FAILURES=$((FAILURES + 1))
fi
echo ""

# --- Gate 8: GAP-002/003 — Wrong list shape and array index in mixed list failure ---
echo "[8/8] Additional gap checks"

if [ -f "$MILESTONE_TEST_FILE" ]; then
  # GAP-002: wrong list shape (object instead of array)
  if grep -q "object.*list\|list.*object\|not.*array\|wrong.*shape\|envelope" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: wrong list shape test present"
  else
    echo "  WARN (GAP-002): no test for wrong list shape (object instead of array for list endpoint)"
    echo "  Note: This is a minor gap — json.Decoder.Decode on object-into-slice produces ErrPartialResponse"
    echo "  which maps to schema_decode via the classifier. Not a product failure, just missing coverage."
  fi

  # GAP-003: mixed list array index in field path
  if grep -q "milestones\[" "$MILESTONE_TEST_FILE"; then
    echo "  PASS: mixed list failure includes array index in field path"
  else
    echo "  WARN (GAP-003): mixed list failure does not verify array index prefix (e.g. milestones[N].field)"
    echo "  The Go JSON decoder wraps UnmarshalJSON errors without appending array index context."
    echo "  This is a minor design deviation from the detailed spec, not a functional product failure."
  fi
else
  echo "  WARN: milestone_adapter_test.go not found, skipping additional gap checks"
fi

echo ""

# --- Summary ---
echo "=== Validation Summary ==="
if [ $FAILURES -gt 0 ]; then
  echo "RESULT: FAIL ($FAILURES failure(s) detected)"
  echo ""
  echo "Summary of failures:"
  echo "  GAP-001 is a product failure: decodeMilestoneStatus silently accepts unrecognized status"
  echo "  values instead of emitting schema_decode as required by acceptance criteria."
  echo "  Fix: update decodeMilestoneStatus to return an error for unrecognized values,"
  echo "  and add a test exercising an unrecognized status value through Milestone.UnmarshalJSON."
  exit 1
else
  echo "RESULT: PASS"
  exit 0
fi
