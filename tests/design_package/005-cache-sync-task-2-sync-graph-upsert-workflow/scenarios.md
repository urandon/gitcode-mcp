# Validation Scenarios: 005 Sync Graph Upsert Workflow

## 005-cache-sync-task-2-sync-graph-upsert-workflow-scenario-1

A developer triggers the product surface `gitcode-mcp sync --repo <repo_id> --issues --wiki --index` with sanitized fixture adapter data, disables network, then runs the CLI read command paths `gitcode-mcp search --repo <repo_id>`, `gitcode-mcp get --repo <repo_id> <record>`, and `gitcode-mcp get-snippet --repo <repo_id> <record>`; the visible CLI responses return at least one issue and one wiki record from the SQLite cache with remote provenance, comments where present, sync events, and chunks.

Concrete offline validation:

- Build and run a temporary Go harness outside production source that imports production CLI/service/cache/gitcode packages.
- Create a temporary SQLite cache through `cache.NewSQLiteStore`, then add sanitized repo `fixture-a` through the real CLI command `gitcode-mcp repo add --repo fixture-a --scopes issues,wiki`.
- Execute the real CLI `sync` dispatcher against a production `service.Service` wired to a sanitized fixture GitCode client; the harness intentionally invokes the acceptance-form command `sync --repo fixture-a --issues --wiki --index` and fails if the CLI cannot parse or execute it.
- Disable network by using only the in-process fixture client and `GITCODE_LIVE_TEST=0`; no live GitCode credentials or external provider calls are used.
- Run the CLI read command paths `search --repo fixture-a`, `get --repo fixture-a ISSUE-42`, `get --repo fixture-a WIKI-HOME`, and `get-snippet --repo fixture-a ISSUE-42` over the same SQLite cache.
- Verify the visible CLI output and cache state prove at least one issue and one wiki record, `provenance=remote`, issue comments, sync events, remote revisions, and chunks.
- Run `cache-status --repo fixture-a --format json` and fail unless repo-scoped counts are nonzero for records, comments, identity aliases, sync events, remote revisions, and chunks.

## 005-cache-sync-task-2-sync-graph-upsert-workflow-scenario-2

Executable evidence is a fixture-backed CLI integration test exercising those command paths plus store tests proving idempotent repeat sync and import-then-sync provenance preservation.

Concrete offline validation:

- Run targeted Go tests for fixture-backed offline sync/read behavior and sync idempotency replay.
- Run targeted cache store tests for `UpsertSyncGraph` idempotent repeat sync and projection-then-remote-sync provenance preservation.
- Run the CLI harness scenario above so evidence comes from the product CLI dispatch path rather than source inventory.
- Run `go test ./...` to ensure the current checkout compiles and all offline regressions pass.
- Run `git diff --check` to reject whitespace breakage.
- The validation does not use live GitCode credentials, network access, device validation, production source edits, or real provider data.
