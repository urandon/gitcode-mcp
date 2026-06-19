# Validation Scenarios: 004 Repo-scoped Cache Schema Migration

## 004-cache-sync-task-1-repo-scoped-cache-schema-migration-scenario-1

A developer runs fixture-backed migrations and the product surface `gitcode-mcp cache-status --repo <repo_id>` against a temporary SQLite cache; the CLI reports WAL-capable cache state plus repo-scoped row counts for records, comments, identity aliases, sync events, audit rows, snapshots, and chunks, and a two-repository fixture proves colliding aliases resolve only with `repo_id` or return a typed conflict.

Concrete offline validation:

- Build a temporary helper outside the repository that imports production `internal/cache` APIs.
- Open a temporary SQLite cache through `cache.NewSQLiteStore`, forcing production migrations and WAL setup.
- Seed two sanitized repositories, both with alias `issue:42`, using `UpsertRecordGraph`, `UpsertChunk`, and `UpsertSnapshot`.
- Verify scoped `ResolveRepoAlias` returns the correct record for each repo.
- Verify unscoped `ResolveAlias(issue:42)` fails with `ErrAliasConflict` or `ErrUnscopedAliasResolution` and never returns arbitrary first-match data.
- Run the real product CLI command `go run ./cmd/gitcode-mcp --cache-path <tmp-db> cache-status --repo fixture-a --format json`.
- Parse the CLI JSON and fail unless it reports `wal_capable: true`, a WAL-compatible journal mode, and repo-scoped counts for records, comments, identity aliases, sync events, audit rows, snapshots, snapshot chunks, chunks, and remote revisions.
- Run the same CLI command for `fixture-b` and fail if fixture-a rows leak into fixture-b counts.

## 004-cache-sync-task-1-repo-scoped-cache-schema-migration-scenario-2

Executable evidence is Go migration/store tests plus a CLI cache-status integration test using sanitized fixtures.

Concrete offline validation:

- Run targeted Go migration/store tests for schema version 2, repo-scoped record counts, provenance constraints, snapshot chunk persistence, and alias collision behavior.
- Run targeted CLI tests for `cache-status` dispatch and JSON rendering.
- Run `go test ./...` to ensure the current checkout compiles and existing offline regression tests pass.
- Run `git diff --check` to reject whitespace breakage.
- The validation does not use live GitCode credentials, network access, or real provider data.
