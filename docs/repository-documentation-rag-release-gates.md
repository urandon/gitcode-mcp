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
- Bare repositories, linked worktrees, and symlinked worktree paths resolve to
  stable opaque Git/worktree identities without exposing filesystem paths.
- Full-text search succeeds with no provider and no semantic revision set.
- Hybrid search selects one exact revision set and transparently reports lexical
  fallback for missing/stale semantic coverage, with stable typed warning
  codes as well as compatible warning text.
- Dirty-worktree behavior is opt-in, tracked-only, and rejects stale overlay
  bytes. Rename-identical and later-committed content reuse vectors.
- Repository aliases produce no parallel revision-set identity.
- A daemon-owned repository-doc job participates in cache writer admission,
  coalescing, cancellation, terminal job retention, durable vector-only replay,
  generation fencing, and metadata GC.
- Explicit cancellation survives restart without reconciliation relaunch, and
  a failed tombstone write returns an error without signalling the worker.
  Repeated failures do not bypass the recorded retry window and resume with the
  same public job identity when that window opens.
- Provider output fetched before writer contention or request cancellation is
  replayed from the vector-only checkpoint without a second provider call.
- Corrupt checkpoints recover as a cache miss; age, byte, and orphan pruning
  keep the vector-only handoff bounded. Orphan detection is derived from
  metadata-backed building/partial/blocked membership with no published vector.
- An in-flight exact historical search completes from its transactional
  snapshot even if metadata retention evicts that historical set afterward.
- Schema v18→v19 preserves existing revision/chunk membership metadata while
  new exact-source fields fail closed until a current generation is published.
- Public CLI/MCP/Admin operations resolve only an explicit opaque source
  selector; compare-and-swap rebind invalidates the old generation.
- A raw SQLite-file sentinel scan finds no document or chunk text.
- Admin UI exposes Documentation navigation/status, pathless registered-source
  reconcile/index, scoped job supervision, exact-revision search and CLI
  handoffs; it survives empty and partial state, and regenerated assets are
  committed.
- CLI and repository-document MCP query/job lifecycle tools expose stable JSON/structured output without raw
  document bodies in status/job diagnostics.

## Performance and storage checks

Run the chunking and full-text retrieval benchmarks twice on the same host.
Record toolchain, CPU, corpus size, `ns/op`, `B/op`, and allocations in the
pull-request handoff. The benchmark is bounded synthetic input and must not
write source text to SQLite. The V1 comparable-host budgets are:

- chunking throughput at least 100 MiB/s;
- full-text retrieval over the 96-document multilingual synthetic fixture in
  less than 2 seconds with fewer than 32 MiB allocated per query;
- deterministic RU/ZH/EN lexical and hybrid top-1 citation accuracy of 3/3;
- an unchanged immediate reindex reuses 100% of eligible vectors.

The 2026-08-30 Apple M4 Pro / darwin-arm64 one-iteration baseline was
165.67 MiB/s and 2.06 MiB allocated for chunking, and 865 ms with 8.55 MiB
allocated for full-text retrieval. These are regression evidence, not portable
absolute performance promises.

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

- Run policy/plan/index/status/search from unrelated working directories with
  the same opaque selector; verify cwd is irrelevant and the same Git store is
  resolved. Verify the previous selector fails after an explicit rebind.
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
