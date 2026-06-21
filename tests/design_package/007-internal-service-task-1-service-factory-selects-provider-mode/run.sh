#!/usr/bin/env bash
set -euo pipefail

# Validation script for task 007: Service factory selects provider mode
# Runs the service-layer tests that verify:
#   1. New(store) delegates to fixture mode (offline, no network)
#   2. NewWithMode with ProviderModeFixture works
#   3. NewWithMode with ProviderModeLive works against httptest server
#   4. NewWithMode with ProviderModeUnavailable / live-no-token fails correctly
#   5. NewWithClient sets provider mode

echo "=== Running service package tests ==="
go test ./internal/service/... -v -count=1 -run "TestNewDelegatesToFixture|TestNewWithModeFixture|TestNewWithModeLive|TestNewWithModeUnavailable|TestNewWithClientSetsProviderMode" -timeout 60s

echo ""
echo "=== Running full repo tests (must pass offline) ==="
go test ./... -count=1 -timeout 180s

echo ""
echo "=== Verifying decommission: sanitizedFixtureClient no longer the only provider path ==="
# sanitizedFixtureClient should still exist as internal implementation but
# New() now delegates to NewWithMode which selects based on ProviderMode,
# meaning NewWithMode with live provider constructs go.gitcode.HTTPClient instead.
# The key check: NewWithMode(ProviderModeLive, token, config) does NOT use sanitizedFixtureClient.
# This is confirmed by TestNewWithModeLive using httptest server with real HTTP calls.

# Verify that sanizedFixtureClient still exists (it's the fixture implementation, not decommissioned)
if grep -q "sanitizedFixtureClient" internal/service/service.go; then
  echo "PASS: sanitizedFixtureClient remains as fixture implementation"
else
  echo "FAIL: sanitizedFixtureClient missing from service.go"
  exit 1
fi

# Verify New delegates to NewWithMode (evidenced by NewWithMode in New's body)
if grep -A5 '^func New(' internal/service/service.go | grep -q "NewWithMode"; then
  echo "PASS: New delegates to NewWithMode for provider selection"
else
  echo "FAIL: New does not delegate to NewWithMode"
  exit 1
fi

# Verify NewWithMode exists with ProviderModeLive path to go.gitcode.HTTPClient
if grep -A20 'ProviderModeLive' internal/service/service.go | grep -q "NewHTTPClient"; then
  echo "PASS: NewWithMode ProviderModeLive constructs go.gitcode.HTTPClient"
else
  echo "FAIL: NewWithMode ProviderModeLive does not construct go.gitcode.HTTPClient"
  exit 1
fi

# Verify ProviderMode method exists
if grep -q 'func (s \*Service) ProviderMode()' internal/service/service.go; then
  echo "PASS: Service.ProviderMode() method exists"
else
  echo "FAIL: Service.ProviderMode() method missing"
  exit 1
fi

# Verify ServiceConfig struct exists and has no Token field
echo ""
echo "=== Verifying ServiceConfig has no Token field ==="
if grep -q 'type ServiceConfig struct' internal/service/types.go; then
  echo "PASS: ServiceConfig struct exists"
else
  echo "FAIL: ServiceConfig struct missing"
  exit 1
fi

# Extract the ServiceConfig struct and check it has no Token/secret field
SVC_CFG_CONTENT=$(sed -n '/^type ServiceConfig struct/,/^}/p' internal/service/types.go)
if echo "$SVC_CFG_CONTENT" | grep -qi 'token\|secret\|password\|key'; then
  echo "FAIL: ServiceConfig appears to contain a credential field"
  exit 1
else
  echo "PASS: ServiceConfig has no raw credential field"
fi

echo ""
echo "=== All validations passed ==="
