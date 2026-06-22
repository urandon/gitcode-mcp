#!/usr/bin/env bash
set -euo pipefail

# --- Validation runner for task 016 ---
# Runs the production-level tests that exercise ZeroDelta, then validates
# the cache migration (v5->v6 adding zero_delta column).
# All tests are offline and deterministic, no network or credentials needed.

cd "$(dirname "$0")/../../.."

echo "=== Scenario ZD-01 & ZD-03: TestZeroDeltaPersistentEvent ==="
go test -run TestZeroDeltaPersistentEvent -count=1 -v ./internal/service/ 2>&1

echo ""
echo "=== Scenario ZD-02: TestZeroDeltaFalseWhenContentChanges ==="
go test -run TestZeroDeltaFalseWhenContentChanges -count=1 -v ./internal/service/ 2>&1

echo ""
echo "=== Scenario ZD-06: TestMigrationZeroDelta (v5->v6 column) ==="
go test -run TestMigrationZeroDelta -count=1 -v ./internal/cache/ 2>&1

echo ""
echo "=== Scenario ZD-05: SyncStatus review via TestZeroDeltaPersistentEvent ==="
# The SyncStatus summary ZeroDelta assertion is inside TestZeroDeltaPersistentEvent.
# Run it explicitly to confirm.
go test -run TestZeroDeltaPersistentEvent -count=1 ./internal/service/ 2>&1

echo ""
echo "=== Full test suite (offline confirmation) ==="
go test ./... 2>&1

echo ""
echo "PASS: All ZeroDelta validation scenarios passed."
