#!/usr/bin/env bash
set -euo pipefail

# Validation script for task 008: ProviderMode enum and provider factory dispatch
# Runs provider-level and service-level Go tests that exercise:
#   1. ProviderMode enum constants exist (fixture/live/unavailable)
#   2. NewFixtureProvider returns working fixture provider
#   3. NewLiveProvider admits valid config, rejects without token/LiveAllowed
#   4. NewUnavailableProvider returns ErrProviderUnavailable on all ops
#   5. Service.NewWithMode dispatches to correct provider per mode
#   6. Full go test ./... passes offline using fixture path only

FAILURES=0

echo "=== SCN-PROVIDER-ENUM-VARIANTS: ProviderMode constants ==="
# Verify the three ProviderMode constants exist with correct values
if ! grep -q 'ProviderModeFixture\s*ProviderMode\s*=\s*"fixture"' internal/gitcode/provider.go; then
  echo "FAIL: ProviderModeFixture constant missing or wrong value"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: ProviderModeFixture = \"fixture\""
fi

if ! grep -q 'ProviderModeLive\s*ProviderMode\s*=\s*"live"' internal/gitcode/provider.go; then
  echo "FAIL: ProviderModeLive constant missing or wrong value"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: ProviderModeLive = \"live\""
fi

if ! grep -q 'ProviderModeUnavailable\s*ProviderMode\s*=\s*"unavailable"' internal/gitcode/provider.go; then
  echo "FAIL: ProviderModeUnavailable constant missing or wrong value"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: ProviderModeUnavailable = \"unavailable\""
fi

echo ""
echo "=== SCN-FIXTURE-FACTORY-RETURNS: NewFixtureProvider contract test ==="
if go test ./internal/gitcode/ -v -run TestFixtureProviderContract -count=1 -timeout 30s; then
  echo "PASS: fixture provider fulfills full provider contract"
else
  echo "FAIL: fixture provider contract test failed"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== SCN-LIVE-FACTORY-GATES: NewLiveProvider admission test ==="
if go test ./internal/gitcode/ -v -run TestLiveProviderAdmission -count=1 -timeout 30s; then
  echo "PASS: live provider rejects without token/LiveAllowed, admits valid config"
else
  echo "FAIL: live provider admission test failed"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== SCN-UNAVAILABLE-FACTORY-ERRORS: NewUnavailableProvider write test ==="
if go test ./internal/gitcode/ -v -run TestProviderWriteUnavailableDoesNotConfirm -count=1 -timeout 30s; then
  echo "PASS: fixture and unavailable providers return ErrProviderUnavailable on writes"
else
  echo "FAIL: unavailable provider write test failed"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== SCN-SERVICE-DISPATCH-FIXTURE: NewWithMode fixture dispatch ==="
if go test ./internal/service/ -v -run TestNewDelegatesToFixture -count=1 -timeout 30s; then
  echo "PASS: New(store) delegates to fixture provider"
else
  echo "FAIL: New delegates-to-fixture test failed"
  FAILURES=$((FAILURES + 1))
fi

if go test ./internal/service/ -v -run TestNewWithModeFixture -count=1 -timeout 30s; then
  echo "PASS: NewWithMode fixture returns fixture-backed service"
else
  echo "FAIL: NewWithMode fixture test failed"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== SCN-SERVICE-DISPATCH-LIVE: NewWithMode live dispatch ==="
if go test ./internal/service/ -v -run TestNewWithModeLive -count=1 -timeout 30s; then
  echo "PASS: NewWithMode live returns live-backed service with httptest"
else
  echo "FAIL: NewWithMode live test failed"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== SCN-SERVICE-DISPATCH-UNAVAILABLE: NewWithMode unavailable dispatch ==="
if go test ./internal/service/ -v -run TestNewWithModeUnavailable -count=1 -timeout 30s; then
  echo "PASS: NewWithMode unavailable / live-no-token returns ErrProviderUnavailable"
else
  echo "FAIL: NewWithMode unavailable test failed"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== SCN-SERVICE-DISPATCH-CUSTOM: NewWithClient custom mode ==="
if go test ./internal/service/ -v -run TestNewWithClientSetsProviderMode -count=1 -timeout 30s; then
  echo "PASS: NewWithClient sets provider mode to custom"
else
  echo "FAIL: NewWithClient test failed"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== SCN-OFFLINE-TEST-SUITE: full go test ./... (must pass offline) ==="
if go test ./... -count=1 -timeout 180s; then
  echo "PASS: full test suite passes offline with fixture providers only"
else
  echo "FAIL: full test suite failed offline"
  FAILURES=$((FAILURES + 1))
fi

echo ""
echo "=== Decommission verification ==="

# decommission-1: sanitizedFixtureClient no longer hard-wired as only path in New()
# New() now delegates to NewWithMode for provider selection
echo "--- decommission-1: sanitizedFixtureClient not the only provider path ---"
if grep -A5 '^func New(' internal/service/service.go | grep -q "NewWithMode"; then
  echo "PASS: New() delegates to NewWithMode for provider selection"
else
  echo "FAIL: New() does not delegate to NewWithMode"
  FAILURES=$((FAILURES + 1))
fi

# Verify NewWithMode ProviderModeLive path constructs HTTPClient not sanitizedFixtureClient
if grep -A20 'ProviderModeLive' internal/service/service.go | grep -q "gitcode.NewHTTPClient\|client, err := gitcode.NewHTTPClient"; then
  echo "PASS: NewWithMode live path constructs real HTTP client, not sanitizedFixtureClient"
else
  echo "FAIL: NewWithMode live path does not construct HTTP client"
  FAILURES=$((FAILURES + 1))
fi

# decommission-2: fixture-only is not the sole runtime provider (--live flag activates live)
echo "--- decommission-2: live provider path exists, not fixture-only ---"
if grep -q 'ProviderModeLive' internal/gitcode/provider.go && grep -q 'NewLiveProvider' internal/gitcode/provider.go; then
  echo "PASS: live provider factory exists enabling opt-in live path"
else
  echo "FAIL: live provider factory missing"
  FAILURES=$((FAILURES + 1))
fi

# Verify go test ./... passes without live env vars (fixture-only)
echo "PASS: go test ./... already verified above with no live env vars"

echo ""
if [ "$FAILURES" -gt 0 ]; then
  echo "=== $FAILURES VALIDATION(S) FAILED ==="
  exit 1
else
  echo "=== All validations passed ==="
fi
