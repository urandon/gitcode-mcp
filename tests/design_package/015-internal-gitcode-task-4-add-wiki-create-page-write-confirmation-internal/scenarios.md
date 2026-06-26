# Design Package Validation Scenarios: Task 015

## Scope

Task: `015-internal-gitcode-task-4-add-wiki-create-page-write-confirmation-internal`

This validation exercises offline Go tests for wiki create-page write confirmation in `internal/gitcode`, using `httptest.Server` as the external GitCode provider mock. It also runs service-level write caching evidence with an in-memory cache and the required repository gates.

## Scenarios

### 015-internal-gitcode-task-4-add-wiki-create-page-write-confirmation-internal-scenario-1

Create-page POST returns 201 with missing path/sha -> follow-up GET confirms with matching content+sha -> record cached with http_attempted: true.

Executable coverage:

- `go test ./internal/gitcode -run '^TestScenario015WikiCreatePageFollowupConfirmation$' -count=1` — mocked POST `201 {}` is followed by GET `/api/v5/repos/{owner}/{repo}.wiki/contents/Home.md`; request body is base64 encoded with no create-time sha; confirmed result carries normalized path, matching body, sha, and provider status.
- `go test ./internal/service -run '^TestS018LiveWriteConfirmedRefreshesCommentAndWiki$' -count=1` — live write service path stores a confirmed wiki page in cache after provider contact, exercising the cached-success surface used for `http_attempted: true` live write diagnostics.

### 015-internal-gitcode-task-4-add-wiki-create-page-write-confirmation-internal-scenario-2

Follow-up GET returns mismatched content or fails -> write_confirmation_incomplete diagnostic, record not cached as definitely written.

Executable coverage:

- `go test ./internal/gitcode -run '^TestScenario015WikiCreatePageFollowupConfirmationFailure$' -count=1` — mocked confirmation GET covers 404, 500, path mismatch, missing sha, and content mismatch; each case returns `ErrWriteConfirmationIncomplete` with diagnostic code `write_confirmation_incomplete` and an unconfirmed/empty write result.

## Repository Gates

- `go test ./internal/gitcode/... -count=1`
- `go test ./internal/service/... -count=1`
- `go test ./...`
- `git diff --check`

## Offline Determinism

The validation uses Go tests with mocked HTTP servers for the external GitCode API and in-memory SQLite for service cache evidence. It does not perform live network, external-provider, credential, or device access.
