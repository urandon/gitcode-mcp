# Scenario 009: Live GitCode REST API adapter package

## SC-009-01: Adapter package compiles and exports contract types
- **Given** the `internal/provider/live/` package exists at architecture-specified location
- **When** `go build ./internal/provider/live/...` is executed
- **Then** the package compiles without errors, and all type aliases (`Client`, `Provider`, `Config`, `ErrProviderUnavailable`, request/response types) resolve to the canonical `internal/gitcode` types

## SC-009-02: NewLiveProvider admission gates
- **Given** `NewLiveProvider` is called with valid config (Mode="live", LiveAllowed=true, Token="test-token")
- **When** the constructor executes
- **Then** a non-nil `Provider` is returned, delegating to `gitcode.NewLiveProvider`

## SC-009-03: NewLiveProvider rejects unauthorized access
- **Given** `NewLiveProvider` is called with `LiveAllowed=false`, empty token, or Mode="fixture"
- **When** the constructor executes
- **Then** `ErrProviderUnavailable` is returned for all three rejection paths

## SC-009-04: NewHTTPClient constructor delegation
- **Given** `NewHTTPClient` is called with valid `HTTPClientConfig`
- **When** the constructor executes
- **Then** a non-nil `*gitcode.HTTPClient` is returned, delegating to `gitcode.NewHTTPClient` with translated config

## SC-009-05: IsProviderUnavailable delegation
- **Given** an `ErrProviderUnavailable` error from `internal/gitcode`
- **When** `live.IsProviderUnavailable(err)` is called
- **Then** returns `true` for provider-unavailable errors and `false` for unrelated errors

## SC-009-06: Offline test determinism
- **Given** no live env vars or `--live` flags set
- **When** `go test ./...` is executed
- **Then** all packages pass, and no existing tests are broken

## SC-009-07: Live sync acceptance (decommission-4)
- **Given** a valid GitCode token is configured via `GITCODE_TOKEN` env var
- **When** `sync --live` is dispatched through the live provider adapter
- **Then** the live provider fetches real issue/wiki/comment records from GitCode API (not fixture-shaped records like ISSUE-42, WIKI-HOME)

## SC-009-08: Live write acceptance (decommission-5)
- **Given** a valid GitCode token is configured
- **When** `create-issue --live` is dispatched through the live provider adapter
- **Then** the issue is created on the remote; no 'fixture client is read-only' error path remains for live writes

## SC-009-09: Rate-limit and auth-failure diagnostics
- **Given** 429 response from GitCode API
- **When** the live provider handles the response
- **Then** rate-limit is reported with clean exit (not a crash)
- **Given** 401/403 response from GitCode API
- **When** the live provider handles the response
- **Then** clear auth-failure diagnostic is produced
