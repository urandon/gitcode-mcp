# Repository documentation RAG release gates

Repository documentation RAG ships only when exact-revision correctness,
metadata-only storage, bounded execution, and every product surface remain
reviewable together.

## Automated gates

Run from the repository root:

```sh
go test ./...
go test -race ./internal/repositorydocs ./internal/cache ./internal/servicectl ./internal/mcp
go vet ./...
go test ./internal/repositorydocs -run 'Test.*(Historical|Offline|Worktree|Stale|Rename|Reuse)'
go test ./internal/cache -run 'TestRepositoryDocument|TestResolveRepositoryBinding'
go test ./internal/servicectl -run TestRepositoryDocsIndexJob
go test ./internal/mcp -run 'TestMCPRepositoryDocumentation|TestMCPRAGCapabilitiesComeFromRegistry'
go test ./internal/adminhttp ./internal/servicectl -run 'TestAdmin|TestObservation'
go test ./internal/repositorydocs -bench RepositoryDocs -benchmem
cd web
npm run licenses
npm run check
npm test
npm run test:e2e -- --workers=1
cd ..
./scripts/check-admin-ui-assets.sh
git diff --check
```

The default suite must pass with network disabled. No test may require GitCode,
fetch missing Git objects, install/start a provider, access credentials, or
persist an absolute worktree path in a public snapshot.

## Required evidence

- Built-in and committed policy resolve deterministically; invalid policy fails
  closed and the published YAML fixture is parsed by tests.
- Historical queries still cite the requested commit after `HEAD` moves.
- Full-text search succeeds with no provider and no semantic revision set.
- Hybrid search selects one exact revision set and transparently reports lexical
  fallback for missing/stale semantic coverage.
- Dirty-worktree behavior is opt-in, tracked-only, and rejects stale overlay
  bytes. Rename-identical and later-committed content reuse vectors.
- Repository aliases produce no parallel revision-set identity.
- A daemon-owned repository-doc job participates in cache writer admission,
  coalescing, cancellation, terminal job retention, and metadata GC.
- A raw SQLite-file sentinel scan finds no document or chunk text.
- Admin UI exposes Documentation navigation/status, pathless registered-source
  reconcile/index, scoped job supervision, exact-revision search and CLI
  handoffs; it survives empty and partial state, and regenerated assets are
  committed.
- CLI and all four MCP tools expose stable JSON/structured output without raw
  document bodies in status/job diagnostics.

## Performance and storage checks

Run the chunking benchmark twice on the same host. Record toolchain, CPU, and
`ns/op`, `B/op`, and allocations in the pull-request handoff. The benchmark is
bounded synthetic input and must not write source text to SQLite.

For a representative public repository, record:

1. cold committed-index wall time and embedded/reused/failed counts;
2. immediate repeat-index wall time and reuse ratio;
3. rename-only and commit-only rerun reuse ratio;
4. cache size delta before/after metadata GC;
5. proof that the source sentinel is absent from the SQLite file.

Regressions are release blockers when exactness changes, source bytes appear in
cache, a routine read starts network/provider setup, an alias forks identity,
or repeated unchanged indexing fails to reuse all eligible vectors. Timing
changes require an issue comment when they exceed 25% on a comparable host.

## Manual product checks

- Run policy/plan/index/status/search from the repository root and a nested
  directory; verify both resolve the same Git store.
- Repeat status/search at a tag and with an explicit tracked overlay.
- Inspect job output, diagnostics, Admin snapshot, copied handoffs, and deep
  links for absolute paths, document bodies, credentials, or provider endpoints.
- Use the Admin Documentation tab at desktop and 390 px widths, in Light, Dark,
  and System themes, with keyboard-only navigation and reduced motion.
- Confirm Admin requires CLI for initial filesystem registration, then accepts
  only opaque registration identity for reconcile/index and exact-revision
  search. Verify request/response payloads contain no absolute path.
- Restart the daemon during indexing; verify the retained job is interrupted
  and a fresh request resumes or safely republishes the revision set.

See [Repository documentation RAG](repository-documentation-rag.md) for the
runtime contract.
