# Validation Scenarios: 008-gitcode-adapter-task-2-write-confirmation-add

## 008-gitcode-adapter-task-2-write-confirmation-add-scenario-1

A developer invokes `CreateIssue`, `UpdateIssue`, `CreateIssueComment`, `CreateWikiPage`, and `UpdateWikiPage` through the adapter API against a stubbed external provider; each valid 2xx response returns `Confirmed=true` with operation, target, required remote identity, idempotency key, response hash, provider status, and confirmed timestamp, evidenced by adapter API tests.

Concrete offline validation:

1. Run `go test ./internal/gitcode -run 'TestConfirmedWriteOperations|TestWriteIdempotency|TestWriteUsesEndpointBuilders' -count=1 -v` with live-test and token environment variables cleared.
2. `TestConfirmedWriteOperations` drives the production `internal/gitcode.HTTPClient` write methods through local `httptest` provider responses for issue create, issue update, issue comment create, wiki create, and wiki update.
3. Each subtest asserts `Confirmed=true`, operation, target, provider status, idempotency key, response hash, confirmed timestamp, provider payload fingerprint, and the operation-specific remote identity minima.
4. `TestWriteIdempotency` asserts generated and supplied idempotency keys are sent to the provider and preserved in confirmed results.
5. `TestWriteUsesEndpointBuilders` asserts write methods route through the production endpoint builders rather than a fake success path.

## 008-gitcode-adapter-task-2-write-confirmation-add-scenario-2

A developer invokes the same API for conflict, auth, forbidden, rate-limit, network, malformed success response, validation failure, and unavailable-provider scenarios; each returns the matching typed error with no confirmed result, no decoded success-shaped record, and no audit-ready metadata, evidenced by stubbed-external-provider tests.

Concrete offline validation:

1. Run `go test ./internal/gitcode -run 'TestWriteNegativeScenariosDoNotConfirm|TestProviderWriteUnavailableDoesNotConfirm|TestWriteIdempotency/conflict' -count=1 -v` with live-test and token environment variables cleared.
2. `TestWriteNegativeScenariosDoNotConfirm` drives validation failure, conflict, auth expired, forbidden, rate limit, network unavailable, malformed success JSON, and malformed confirmation-minima scenarios through the production write API.
3. Each negative path asserts the matching typed error and requires no confirmation, no idempotency key, no response hash, and no confirmed timestamp on the returned result.
4. `TestWriteIdempotency/conflict` asserts conflict payload evidence is redacted before exposure.
5. `TestProviderWriteUnavailableDoesNotConfirm` asserts fixture and unavailable providers reject writes as provider-unavailable and never return success-shaped metadata.

## 008-gitcode-adapter-task-2-write-confirmation-add-scenario-3

A CLI/write owner requests provider selection through product runtime config; success mocks are not selectable while unavailable providers cannot be audited as success, evidenced by CLI/API tests with external-provider mocks and provider-selection tests.

Concrete offline validation:

1. Run `go test ./internal/gitcode -run 'TestProviderWriteUnavailableDoesNotConfirm|TestLiveProviderAdmission|TestFixtureProviderContract' -count=1 -v` with live-test and token environment variables cleared.
2. `TestLiveProviderAdmission` proves the product live provider can only be admitted by explicit live mode, live allowance, and token, and rejects unavailable configurations before HTTP construction.
3. `TestFixtureProviderContract` proves the fixture provider is a read provider on the production `Provider` seam.
4. `TestProviderWriteUnavailableDoesNotConfirm` proves product-selectable fixture and unavailable providers cannot be audited as write success.
5. The validation script additionally runs a compile-time product-selection guard that references only `ProviderModeLive`, `ProviderModeFixture`, `ProviderModeUnavailable`, `NewLiveProvider`, `NewFixtureProvider`, and `NewUnavailableProvider`; any product-selectable success-mock constructor or mode would need to be intentionally added and covered by production tests before this validation can pass.

## Full scenario command

Run `tests/design_package/008-gitcode-adapter-task-2-write-confirmation-add/run.sh` from the repository root. The script disables live validation, clears token variables, runs the targeted adapter/provider scenario tests through local stubbed providers, then runs `go test ./internal/gitcode ./internal/testnet`, `go test ./...`, and `git diff --check` for current-source evidence.
