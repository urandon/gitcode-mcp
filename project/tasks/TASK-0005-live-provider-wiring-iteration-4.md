# TASK-0005: Live provider wiring iteration 4

Status: ready

## Goal

Make `gitcode-mcp --live` usable through the real CLI/startup path, with offline tests proving provider selection before any credential-gated real GitCode smoke.

Iteration 4 should close the remaining live-readiness gaps from the iteration 3 smoke:

- `sync --live` must not silently use fixture data.
- `create-issue --live` must not reach `fixture client is read-only`.
- Keychain-resolved credentials must be usable by live sync/write, not only by `auth status`.
- A mocked GitCode API must exercise the same CLI path an operator runs.

## Scope

1. Add an offline live CLI integration test using `httptest.Server` as a GitCode-compatible API.
2. Route CLI/startup live mode through one credential resolver and one provider construction path.
3. Make live sync/write use the live HTTP client under `--live`, and fail clearly when credentials are missing.
4. Clarify how API base URL is selected for live commands: global config/env and repository binding must not disagree silently.
5. Keep default `sync` and `go test ./...` fixture/offline.

## Required Tests

Add tests that run the real command path, not only `service.NewWithClient`.

Required cases:

- `sync --live` with no token returns missing-credential and makes zero requests to the mock server.
- `sync --live` with an invalid token reaches the mock server and reports auth failure on 401/403.
- `sync --live` with a valid test token populates cache from mock issue/wiki/comment responses and does not create fixture `ISSUE-42` / `WIKI-HOME` records.
- `create-issue --live` reaches the mock server, writes audit/cache confirmation, and never returns `fixture client is read-only`.
- `unset GITCODE_TOKEN` plus a mocked credential source equivalent to Keychain passes the write credential gate.
- ordinary `sync` without `--live` remains fixture-backed and does not call the mock server.

Use public-safe fixture names and bodies only.

## API Base URL Contract

The implementation must document and test one explicit rule:

- Either live commands use the repository binding `api_base_url` from `repo add --api-base-url`;
- or live commands use global `gitcode_base_url` / `GITCODE_API_URL`, and `repo add --api-base-url` is treated as binding metadata only.

Do not leave both paths appearing valid while only one actually controls the HTTP client.

## Acceptance

- `go test ./...` passes without real credentials or network.
- New offline live CLI tests fail if `--live` routes to fixtures.
- New offline live CLI tests fail if `auth status` can see a credential but live write cannot.
- Manual live smoke against a real test repository is reduced to a final credential-gated check, not the primary proof of provider wiring.
- `doctor --live` reports the effective provider mode, credential source, cache path, and API base URL used by the live client.

## Out of Scope

- New GitCode API discovery beyond endpoints already represented by the mock server.
- Large schema migrations.
- New MCP protocol behavior.
- Real live e2e as a default test; keep it explicitly gated.

## Validation Commands

```sh
go test ./...
git diff --check
```

Optional credential-gated smoke:

```sh
go test -run TestE2ELiveTwoCache -tags=e2e ./internal/e2e/
```
