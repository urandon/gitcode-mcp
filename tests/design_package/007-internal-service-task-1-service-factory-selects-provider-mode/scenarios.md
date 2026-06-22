# Validation Scenarios

## 007-internal-service-task-1-service-factory-selects-provider-mode-scenario-1

A service-layer Go test constructs `New(store)` with an in-memory or temporary `cache.Store`, calls `SyncToCache`, and observes fixture records without network access; executable evidence is `go test ./internal/service` and default `go test ./...` with no live env vars.

**Covered by:** `TestNewDelegatesToFixture` in `internal/service/service_test.go`

## 007-internal-service-task-1-service-factory-selects-provider-mode-scenario-2

A service-layer Go test constructs `NewWithMode(store, gitcode.ProviderModeFixture, nil, ServiceConfig{})`, calls a sync path, and observes the same fixture-backed state with no error and `ProviderMode()` returning fixture.

**Covered by:** `TestNewWithModeFixture` in `internal/service/service_test.go`

## 007-internal-service-task-1-service-factory-selects-provider-mode-scenario-3

A service-layer Go test constructs `NewWithMode(store, gitcode.ProviderModeLive, resolvedCredential, ServiceConfig{...})` against an external-provider test server or local HTTP test endpoint, calls a read/sync path, and verifies the existing `gitcode.HTTPClient` path is used rather than `sanitizedFixtureClient`.

**Covered by:** `TestNewWithModeLive` in `internal/service/service_test.go`

## 007-internal-service-task-1-service-factory-selects-provider-mode-scenario-4

A service-layer Go test constructs `NewWithMode(store, gitcode.ProviderModeUnavailable, nil, ServiceConfig{})` or live mode with no credential and verifies construction fails with an error recognized as `gitcode.ErrProviderUnavailable` / `gitcode.IsProviderUnavailable`, with no `Service` returned.

**Covered by:** `TestNewWithModeUnavailable` in `internal/service/service_test.go`
