# Complete CLI read handlers validation scenarios

- `012-cli-read-task-1-complete-cli-read-handlers-scenario-1`: After a developer runs fixture sync/index and disables network access, invoking the production CLI entrypoint with `--repo <repo_id>` for canonical `list`, `search`, `get`, `get-snippet`, `backlinks`, `recent`, `link-check`, `stale-index`, `cache-status`, `list-chunks`, `export`, and `diff` returns deterministic repo-scoped text and JSON output.
  - Build an offline sanitized fixture cache with issue and wiki records, sync metadata, deterministic chunks, links, and a second repository with a colliding issue alias.
  - Invoke `go run ./cmd/gitcode-mcp --cache-path <fixture.db>` for every canonical Task 5 read command in JSON mode and representative text mode.
  - Assert repeated invocations are byte-identical for deterministic commands, all payloads are scoped to `fixture-a`, and the commands succeed without credentials or live network configuration.

- `012-cli-read-task-1-complete-cli-read-handlers-scenario-2`: The same integration suite invokes exactly the supported Task 5 aliases `snippet` and `snippets` and verifies they resolve to the same behavior as `get-snippet`.
  - Invoke `get-snippet`, `snippet`, and `snippets` with identical line-range arguments through the production CLI entrypoint.
  - Assert the alias outputs are byte-identical to canonical `get-snippet` output.

- `012-cli-read-task-1-complete-cli-read-handlers-scenario-3`: Expected visible/state outcome is issue and wiki records in list/search/get flows, chunk citations and source ranges for snippet/list-chunks/export flows, row counts and index freshness in cache-status, stored snapshot behavior in export/diff, and explicit stale or missing-index warnings instead of silent omissions.
  - Assert list/search/get expose both issue and wiki fixture records.
  - Assert snippet/list-chunks/export include chunk IDs, citations or chunk text, source IDs, source ranges, content hashes, and repo IDs.
  - Assert cache-status reports row counts and index freshness warning fields.
  - Export a stored snapshot file, diff against that stored file, and assert deterministic diff metadata.
  - Build a separate missing-index fixture cache and assert stale-index, cache-status, list-chunks, get-snippet, and export expose warning metadata instead of silently omitting index state.

- `012-cli-read-task-1-complete-cli-read-handlers-scenario-4`: Executable evidence is a CLI integration test over sanitized fixtures through the production CLI entrypoint that verifies missing `--repo` returns `validation-failed`, unknown repo returns `not-found`, cross-repo alias collision is rejected or disambiguated by repo/cache services, text/JSON ordering and fields are deterministic, and no read command performs live network access.
  - Invoke missing-repo and unknown-repo error paths in JSON mode and assert typed failure classes (`repo_required` as the current validation failure class for missing repo, and `not_found` for unknown repo).
  - Assert `issue:42` resolves to different records when scoped to `fixture-a` and `fixture-b`, and unscoped alias lookup is rejected.
  - Run the full suite with no GitCode token and an invalid API URL to ensure read commands remain cache-first and offline.
