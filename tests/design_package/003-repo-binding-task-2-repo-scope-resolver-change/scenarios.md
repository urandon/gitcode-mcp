# Validation Scenarios: 003 Repo Scope Resolver Change

## 003-repo-binding-task-2-repo-scope-resolver-change-scenario-1

After two fixture repositories are added and both contain record alias `issue:42`, a developer runs a CLI read with `--repo fixture-a issue:42` and receives only fixture-a data with response metadata containing `repo_id: fixture-a`; the same command with `--repo fixture-b issue:42` returns only fixture-b data.

Concrete offline validation:

- Run the targeted CLI test `TestCLIRepoScopedDuplicateAlias`.
- The test builds an in-memory cache with `fixture-a` and `fixture-b`, both with `issue:42`.
- It exercises the production CLI dispatcher for `get --repo fixture-a issue:42` and `get --repo fixture-b issue:42`.
- It fails if either response omits the scoped `repo_id` metadata or includes the other repository's body.

## 003-repo-binding-task-2-repo-scope-resolver-change-scenario-2

A developer runs an unscoped product read for `issue:42` and receives `ambiguous_alias` or `repo_required` instead of the first matching source, while a scoped non-colliding lookup such as `--repo fixture-a wiki:Home` resolves successfully.

Concrete offline validation:

- Run the targeted CLI test `TestCLIRepoScopedDuplicateAlias` for the unscoped product-read rejection.
- Run the targeted service runtime test `TestRepoScopedAliasResolution` for scoped `wiki:Home` resolution.
- The validation fails if unscoped CLI lookup succeeds or returns first-match data instead of `repo_required`/typed scoped diagnostic.
- The validation fails if `wiki:Home` does not resolve inside `fixture-a`.

## 003-repo-binding-task-2-repo-scope-resolver-change-scenario-3

An MCP JSON-RPC read tool call with `repo_id: fixture-a` returns the same scoped source as the equivalent CLI read, and snapshot/export selection rejects a source alias from another repo with typed not-found or validation-failed.

Concrete offline validation:

- Run the targeted MCP JSON-RPC test `TestMCPRepoScopedDuplicateAlias`.
- Run the targeted service snapshot/export test `TestRepoScopedAliasResolution`.
- MCP validation uses the production JSON-RPC `tools/call` path for `get_source` with `repo_id` arguments over fixture cache data.
- Snapshot validation exercises `ExportSnapshot` with `repo_id: fixture-a` and a cross-repo source selector, failing unless the selector is rejected with a typed product error.

## 003-repo-binding-task-2-repo-scope-resolver-change-scenario-4

A developer also attempts sync/write routing for a repo whose `wiki` scope is disabled; the repo-binding route rejects the operation before any live adapter call, proven by a fixture or stubbed-adapter test that records zero adapter invocations.

Concrete offline validation:

- Run the targeted service tests `TestSyncRejectsDisabledWikiScopeBeforeAdapter`, `TestBuildAdapterRouteValidatesRepoScope`, and `TestDisabledWikiScopeRejectedBeforeClient`.
- These tests configure an `issues-only` repo, attempt wiki sync/write routing, and fail if the fake external adapter records any invocation.
- The fake adapter is used only as an external dependency sentinel; the production service route validation path is exercised before adapter handoff.
