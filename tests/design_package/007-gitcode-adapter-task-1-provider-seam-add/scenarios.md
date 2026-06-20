# Validation Scenarios: 007-gitcode-adapter-task-1-provider-seam-add

## 007-gitcode-adapter-task-1-provider-seam-add-scenario-1

A maintainer runs adapter contract tests with no credentials against `NewFixtureProvider`; target API surface `internal/gitcode.Provider` returns deterministic repo/auth, issue list/get, comment list, wiki list/get, search, pagination, auth-error, conflict, and rate-limit responses with no external network, evidenced by `go test ./internal/gitcode ./internal/testnet`.

Concrete offline validation:

1. Run `go test ./internal/gitcode ./internal/testnet -run 'TestFixtureProviderContract|TestFixtureProviderScenarios|TestProviderPaginationGuards|TestNoExternalNetwork' -count=1 -v` with live-test and token environment variables cleared.
2. `TestFixtureProviderContract` constructs `NewFixtureProvider` and exercises the production `Provider` interface for auth probe, repo lookup, issue list/get, comments, wiki list/get, and search.
3. `TestFixtureProviderScenarios` exercises fixture-backed auth-error, conflict, and rate-limit typed failures.
4. `TestProviderPaginationGuards` exercises malformed-page and pagination-loop guard behavior through the fixture provider path.
5. `TestNoExternalNetwork` verifies the testnet guard blocks external HTTP while allowing loopback, so the fixture scenario remains offline.

## 007-gitcode-adapter-task-1-provider-seam-add-scenario-2

A maintainer runs live-provider admission tests; `NewLiveProvider` with token-present-but-live-not-allowed and live-allowed-without-token returns `ErrProviderUnavailable` before `NewHTTPClient`, evidenced by local unit tests using an HTTP-construction sentinel.

Concrete offline validation:

1. Run `go test ./internal/gitcode -run 'TestLiveProviderAdmission' -count=1 -v` with live-test and token environment variables cleared.
2. The test installs an HTTP-construction sentinel through the provider construction hook.
3. The test calls `NewLiveProvider` with a token but without `LiveAllowed` and requires `ErrProviderUnavailable` with no HTTP construction.
4. The test calls `NewLiveProvider` with `LiveAllowed` but without a token and requires `ErrProviderUnavailable` with no HTTP construction.
5. The test then calls the admitted live configuration and requires the sentinel to be reached only after admission passes.

## 007-gitcode-adapter-task-1-provider-seam-add-scenario-3

A maintainer enables `GITCODE_LIVE_TEST=1` with a token against a test provider; live validation either skips safely or writes only `RedactedCapture` artifacts that pass fixture sanitization, evidenced by credential-gated adapter tests.

Concrete offline validation:

1. Run `go test ./internal/gitcode ./internal/testnet -run 'TestRequireLiveProviderForTestGate|TestIntegrationRequireLiveIntegration|TestRedactedCapture|TestSanitizedFixtures' -count=1 -v` with live-test and token environment variables cleared.
2. `TestRequireLiveProviderForTestGate` verifies the live provider gate refuses live validation unless `GITCODE_LIVE_TEST=1` and a token are present, with the transitional token alias covered by unit test only.
3. `TestIntegrationRequireLiveIntegration` verifies live integration checks skip safely when live validation is not explicitly enabled.
4. `TestRedactedCapture` exercises production redaction construction and proves raw token, cookie, owner, repo, and unapproved host values are removed from durable capture fields.
5. `TestSanitizedFixtures` validates repository fixtures for secret/public-safety constraints.

## Full scenario command

Run `tests/design_package/007-gitcode-adapter-task-1-provider-seam-add/run.sh` from the repository root. The script disables live validation, clears token variables, runs the targeted scenario tests above, then runs `go test ./...` and `git diff --check` for current-source evidence.
