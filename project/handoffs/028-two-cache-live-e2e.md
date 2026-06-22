# Handoff: 028 two-cache live e2e test harness

Task: `028-internal-e2e-task-1-add-two-cache-live-e2e-test-harness`

## Implemented

- Added `internal/e2e/two_cache_test.go` behind `//go:build e2e`.
- The test skips unless live opt-in env vars are set.
- The harness creates two temp SQLite caches, binds both to the same repo, syncs aliases, creates one live issue, confirms remote visibility, re-syncs cache A, independently syncs cache B, compares normalized record digests, checks created issue presence, checks comment count parity, and scans recorded output for raw token and `Authorization:` patterns.

## Validation run

- `go test -run TestE2ELiveTwoCache -tags=e2e ./internal/e2e/` — passed via missing-env skip behavior in this environment.
- `go test ./...` — passed offline.
- `git diff --check` — passed.

## Live validation skipped

Live two-cache parity was not executed because this environment does not provide live GitCode credentials or repository coordinates.

To run live validation, set:

- `GITCODE_TOKEN`
- `GITCODE_E2E_OWNER`
- `GITCODE_E2E_REPO`
- optional `GITCODE_E2E_BASE_URL` or `GITCODE_E2E_API_BASE_URL`

Then run:

```sh
go test -run TestE2ELiveTwoCache -tags=e2e ./internal/e2e/
```

## Live side effect

The live test intentionally creates one issue in the configured repository using a generated title and idempotency key. Cleanup of the remote issue is manual if the operator requires it.
