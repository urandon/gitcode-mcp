# Validation Scenarios

## 008-internal-provider-task-1-add-providermode-enum-and-provider-factory-dispatc-scenario-1

ProviderMode enum has fixture/live/unavailable variants. A Go test confirms the three `ProviderMode` string constants exist with exact values `"fixture"`, `"live"`, and `"unavailable"`, and that the type aliases `string`. Executable evidence is `go test ./internal/gitcode/ -v -run TestFixtureProviderContract` and `go test ./internal/gitcode/ -v -run TestLiveProviderAdmission` which exercise all three provider modes.

**Covered by:** `internal/gitcode/provider.go:27-33` (type + constants), `internal/gitcode/provider_test.go` (contract + admission tests)

## 008-internal-provider-task-1-add-providermode-enum-and-provider-factory-dispatc-scenario-2

Factory dispatch returns fixture provider when mode=fixture, live provider when mode=live, unavailable provider when mode=unavailable. `go test ./...` uses fixture path. Executable evidence is `go test ./internal/gitcode/ -v -run "TestFixtureProviderContract|TestLiveProviderAdmission|TestProviderWriteUnavailableDoesNotConfirm"` for provider-layer factories and `go test ./internal/service/ -v -run "TestNewDelegatesToFixture|TestNewWithModeFixture|TestNewWithModeLive|TestNewWithModeUnavailable|TestNewWithClientSetsProviderMode"` for service-layer dispatch, followed by full `go test ./...` with no live env vars.

**Covered by:** `internal/gitcode/provider.go` (`NewLiveProvider`, `NewUnavailableProvider`), `internal/gitcode/fixture_provider.go` (`NewFixtureProvider`), `internal/service/service.go:53-106` (`NewWithMode` switch dispatch)
