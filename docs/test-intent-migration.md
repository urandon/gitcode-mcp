# Test Intent Migration

This note records how the deleted `tests/design_package` tree was reviewed and
repackaged after commit `0389acd` removed generated validation artifacts from
`main`.

## Source Inventory

Historical source: `0389acd^:tests/design_package`.

The tree contained:

- 126 generated validation package directories;
- 388 tracked files;
- 126 `run.sh` wrappers;
- 126 `scenarios.md` intent summaries;
- 126 `validation.json` evidence summaries;
- 7 built `gitcode-mcp` binaries;
- 3 output or lock artifacts.

The generated tree should not be restored as-is. It mixed executable wrappers,
historical evidence, binaries, output snapshots, and package-specific test
intent under one top-level directory. That made it easy to accumulate stale
artifacts and hard to tell which checks were product tests.

## Migration Policy

Use this mapping when reviewing old design-package content:

| Historical artifact | Destination |
| --- | --- |
| Product behavior already covered by package tests | Keep the package test; do not restore the wrapper. |
| Reusable offline HTTP or cache test harness | Move to `internal/testnet` or the owning package helper. |
| Package-specific behavior gap | Add or extend `_test.go` next to the package under test. |
| CLI/MCP parity scenario | Keep in `cmd/gitcode-mcp` or `internal/mcp` package tests. |
| Live E2E scenario | Keep in `internal/e2e` behind `//go:build e2e`. |
| Historical run output, generated binaries, locks | Do not restore; use git history or wiki evidence. |
| Durable testing policy | Document in `docs/test-architecture.md`. |

## Migrated in #18

### `012-mock-gitcode-api-task-1-mockgitcodeapi-harness-validate`

Historical intent:

- exercise the real CLI startup path against an offline GitCode-like HTTP
  dependency;
- prove live sync increments issue/wiki/comment counters without leaking fixture
  records;
- prove missing credentials cause zero HTTP requests;
- prove invalid credentials are classified after an authenticated request;
- prove create-issue captures authorization and idempotency metadata;
- prove repository binding selects the configured API base URL.

Repackaged form:

- `cmd/gitcode-mcp/main_test.go` still owns the CLI product scenarios.
- `internal/testnet/gitcode_api.go` now owns the reusable offline GitCode API
  harness.
- `internal/testnet/testnet_test.go` now verifies harness auth, counters,
  failure modes, and idempotency capture at the helper boundary.

The old generated wrapper, binary, and JSON evidence remain out of `main`.

## Remaining Review Buckets

The historical tree mostly references behavior now covered by package-local
tests in these areas:

- cache source graphs, provenance, writer ownership, schema migration, and
  lock contention;
- GitCode live adapter routes for issues, wiki, labels, milestones, PRs, and
  comments;
- service sync/write/audit/idempotency behavior;
- CLI read/write/help/doctor/config surfaces;
- MCP registry, transport, read parity, write lifecycle, and tool access
  policy;
- auth, credential, diagnostics, redaction, and sanitization surfaces;
- live E2E two-cache behavior behind the `e2e` build tag.

Future cleanups should migrate one historical package at a time. Each migration
should name the historical directory, state whether it was already covered or
repackaged, and add focused package tests when coverage is missing.
