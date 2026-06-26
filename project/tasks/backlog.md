# Backlog

Updated: 2026-06-17

## Active

| ID | Title | Status | Outcome |
| --- | --- | --- | --- |
| TASK-0001 | Discover GitCode tracker and wiki API surface | ready | Identify supported APIs, auth, pagination, issue/wiki CRUD, search, attachments, comments, export behavior, and poor-network failure modes with sanitized fixtures. |
| TASK-0002 | Define local cache schema and source ingest contract | ready | Normalize markdown, tracker, and wiki source exports into cache records with identity map, text, links, backlinks, and sync status. |
| TASK-0003 | Implement cache-first CLI skeleton | ready | Replace current stubs with working `search`, `get`, `link-check`, `export`, `diff`, and `sync status` commands over local cache data. |
| TASK-0004 | Prototype read-only MCP server over local cache | candidate | Expose search/get/backlinks/resolve/sync-status for GitCode data once cache schema is stable. |
| TASK-0005 | Live provider wiring iteration 4 | ready | Prove `--live` provider selection through the real CLI/startup path with mocked GitCode HTTP tests before credential-gated real live smoke. |
| TASK-0006 | Live API coverage iteration 5 | ready | Close live API-shape gaps for labels, milestones, PR/comments, wiki strategy, error classification, and cache provenance using documented or sanitized live evidence. |
| TASK-0007 | Live operations iteration 6 | ready | Turn the iteration 5 smoke findings into an agent-usable live workflow: MCP lifecycle tools, empty-wiki handling, bounded collection sync, credential parity, and write confirmation fixes. |

## Done

| ID | Title | Date | Evidence |
| --- | --- | --- | --- |
| TASK-0000 | Bootstrap repository layout | 2026-06-17 | Initial scaffold with docs, project management folders, Python package, CLI stubs, and tests. |
