# Design Package Validation Scenarios: Task 014

## Scope

Task: `014-internal-gitcode-task-3-change-wiki-path-normalization-internal-gitcode-w`

This validation exercises offline Go tests for wiki path normalization in `internal/service` and `internal/gitcode`, using mocked HTTP servers as external-provider mocks, plus Go service-level tests for the integration tier with in-memory cache.

## Scenarios

### 014-internal-gitcode-task-3-change-wiki-path-normalization-internal-gitcode-w-scenario-1

Remote page named Home.md synced; cached record path field is wiki/Home.md.

Executable coverage:

- `go test ./internal/service -run '^TestWikiPathNormalizationInSync$' -count=1` — end-to-end sync with a fake client returning `Slug: "Home.md"`, asserting cached `Record.Path == "wiki/Home.md"` (not `"wiki/Home.md.md"`).
- `go test ./internal/service -run '^TestNormalizeWikiCachePath$' -count=1` — 10 table-driven cases for `normalizeWikiCachePath`, including `"Home.md" -> "wiki/Home.md"`, subdirectory paths, non-markdown extensions, and extension-less slugs.

### 014-internal-gitcode-task-3-change-wiki-path-normalization-internal-gitcode-w-scenario-2

Existing test fixtures updated. go test ./internal/gitcode/... passes.

Executable coverage:

- `go test ./internal/gitcode/... -count=1` — all gitcode adapter tests pass.
- `go test ./internal/service/... -count=1` — all service tests pass including `TestNormalizeWikiCachePath` and `TestWikiPathNormalizationInSync`.
- `go test ./...` — full repository acceptance gate.
- `git diff --check` — no whitespace violations.

## Offline Determinism

The validation uses Go tests with in-memory SQLite stores and fake GitCode clients (for service tier) plus mocked HTTP servers (for gitcode adapter). It does not perform live network, external-provider, credential, or device access.
