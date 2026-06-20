# Outcome Contract

Schema Version: `triborg.outcome-contract.v1`

## outcome-1
- Request Task: 1
- Role: `supporting_evidence`
- Request Item: Runtime gap audit across current scaffold, docs, implemented runtime behavior, and installed MCP surface.
- Target Surface: CLI command gitcode-mcp doctor --runtime-audit --repo <repo_id>
- Actor/Trigger: Developer runs runtime audit against a configured test repository
- Expected Outcome: CLI reports installed version, active config, repository binding, cache state, MCP surfaces, sync/index readiness, and actionable failure classes
- Evidence Type: CLI command
- Freshness: current source/build/runtime evidence required
- Mock Policy: external_dependencies_only


## outcome-2
- Request Task: 2
- Role: `supporting_evidence`
- Request Item: Repository binding and repo-scoped identity model.
- Target Surface: CLI repo commands and MCP repo-aware filters
- Actor/Trigger: Developer configures two repositories and reads by repo_id
- Expected Outcome: Reads are scoped to the configured repository and colliding aliases are rejected or disambiguated
- Evidence Type: CLI command
- Freshness: current source/build/runtime evidence required
- Mock Policy: external_dependencies_only


## outcome-3
- Request Task: 3
- Role: `supporting_evidence`
- Request Item: Installation, active-config discovery, secrets, credential stores, wrappers, doctor/auth/config commands, and public-safe docs.
- Target Surface: CLI config/auth/doctor commands
- Actor/Trigger: Developer initializes config and checks redacted auth/config state
- Expected Outcome: CLI shows active config path, override sources, token presence without secret value, cache path resolution, and platform-specific next steps
- Evidence Type: CLI command
- Freshness: current source/build/runtime evidence required
- Mock Policy: external_dependencies_only


## outcome-4
- Request Task: 4
- Role: `supporting_evidence`
- Request Item: Cache bootstrap and exact read path from GitCode/test fixtures through indexing to offline CLI/MCP reads.
- Target Surface: CLI sync/index workflow
- Actor/Trigger: Developer syncs issues and wiki for a configured repository
- Expected Outcome: Cache stores issue and wiki records, sync events, index chunks, and offline reads succeed after network is disabled
- Evidence Type: CLI command
- Freshness: current source/build/runtime evidence required
- Mock Policy: external_dependencies_only


## outcome-5
- Request Task: 5
- Role: `supporting_evidence`
- Request Item: Read path treated as a first-class product contract with deterministic CLI reads.
- Target Surface: CLI read commands
- Actor/Trigger: Developer runs offline CLI read commands after fixture sync/index
- Expected Outcome: Commands return repo-scoped deterministic output with issue/wiki coverage and clear stale or missing-index warnings
- Evidence Type: CLI command
- Freshness: current source/build/runtime evidence required
- Mock Policy: no_mocks


## outcome-6
- Request Task: 6
- Role: `supporting_evidence`
- Request Item: MCP query parity for coordinator usage: snippets, recent changes, link checks, stale-index reports, cache status, chunks, backlinks, and sync status.
- Target Surface: MCP JSON-RPC tools
- Actor/Trigger: MCP client invokes existing and new read tools with repo_id filters
- Expected Outcome: MCP responses match equivalent CLI reads for issue/wiki records and cache/index status
- Evidence Type: API test
- Freshness: current source/build/runtime evidence required
- Mock Policy: no_mocks


## outcome-7
- Request Task: 7
- Role: `supporting_evidence`
- Request Item: Shared HTTP SSE MCP server transport for multiple clients, while keeping stdio as local compatibility mode.
- Target Surface: MCP stdio and HTTP/SSE runtime routes
- Actor/Trigger: Developer starts HTTP/SSE MCP server and connects two clients
- Expected Outcome: Two clients query the same cache concurrently, stdio remains available, health/readiness report ready state, and request logs include correlation IDs
- Evidence Type: runtime/compiler test
- Freshness: current source/build/runtime evidence required
- Mock Policy: no_mocks


## outcome-8
- Request Task: 8
- Role: `supporting_evidence`
- Request Item: Database concurrency and lock ownership for one cache database shared by multiple agents and clients.
- Target Surface: SQLite cache runtime and lock manager
- Actor/Trigger: Concurrent clients read while sync/index holds writer ownership
- Expected Outcome: Safe reads continue, conflicting writers receive typed busy/owned errors, and migrations are blocked under ownership conflict
- Evidence Type: runtime/compiler test
- Freshness: current source/build/runtime evidence required
- Mock Policy: no_mocks


## outcome-9
- Request Task: 9
- Role: `supporting_evidence`
- Request Item: Real GitCode write operations for issues, comments, and wiki pages, including safety and idempotency.
- Target Surface: CLI write commands and GitCode adapter API routes
- Actor/Trigger: Developer runs dry-run and stubbed-provider write commands
- Expected Outcome: Dry run performs no mutation, successful writes persist audit/cache state, conflicts return actionable errors, and unavailable adapters cannot report success
- Evidence Type: API test
- Freshness: current source/build/runtime evidence required
- Mock Policy: external_dependencies_only


## outcome-10
- Request Task: 10
- Role: `supporting_evidence`
- Request Item: GitCode tracker/wiki source-of-truth model with local markdown/cache projections.
- Target Surface: CLI import/sync/cache workflow
- Actor/Trigger: Developer imports local markdown projection and then syncs from GitCode
- Expected Outcome: GitCode issue/wiki identities remain canonical, projection provenance is separate, and projection-only aliases are not treated as remote truth
- Evidence Type: CLI command
- Freshness: current source/build/runtime evidence required
- Mock Policy: external_dependencies_only


## outcome-11
- Request Task: 11
- Role: `supporting_evidence`
- Request Item: Snapshot, diff, chunk, citation, corpus export, and deterministic validation integrity.
- Target Surface: CLI index/export-snapshot/diff-snapshot commands
- Actor/Trigger: Developer indexes, exports snapshots, and diffs stored snapshot ids
- Expected Outcome: Exports include chunks and citations or explicit warnings; valid ids resolve; unknown ids return not-found errors
- Evidence Type: CLI command
- Freshness: current source/build/runtime evidence required
- Mock Policy: no_mocks


## outcome-12
- Request Task: 12
- Role: `supporting_evidence`
- Request Item: Extensible chunking policies: heading-delimited default, max-size fallback, and sliding-window option.
- Target Surface: Indexing runtime, CLI and MCP chunk/snippet surfaces
- Actor/Trigger: Developer indexes fixtures with default and sliding-window chunking policies
- Expected Outcome: Chunks are deterministic, cite source ranges, respect max-size settings, and are queryable through CLI and MCP
- Evidence Type: runtime/compiler test
- Freshness: current source/build/runtime evidence required
- Mock Policy: no_mocks


## outcome-13
- Request Task: 13
- Role: `supporting_evidence`
- Request Item: Sanitized fixtures and optional credential-gated live validation against a test GitCode repository.
- Target Surface: Fixture validation and optional live API test commands
- Actor/Trigger: Maintainer runs offline fixture validation and optional credential-gated live validation
- Expected Outcome: Offline fixture tests pass, live tests are skipped unless explicitly enabled, and live responses are redacted before durable writes
- Evidence Type: local command
- Freshness: current source/build/runtime evidence required
- Mock Policy: external_dependencies_only


## outcome-14
- Request Task: 14
- Role: `supporting_evidence`
- Request Item: Documentation deliverables for install, config, repository binding, secrets, MCP setup, walkthroughs, troubleshooting, and fixture capture.
- Target Surface: Public documentation and documented CLI/MCP workflow
- Actor/Trigger: New developer follows docs from install through CLI and MCP reads
- Expected Outcome: Every documented command succeeds or returns documented diagnostics without exposing private paths or secrets
- Evidence Type: local command
- Freshness: current source/build/runtime evidence required
- Mock Policy: external_dependencies_only


## outcome-15
- Request Task: 15
- Role: `supporting_evidence`
- Request Item: One-week implementation plan with tasks ordered for dogfood value.
- Target Surface: Implementation workflow and executable product checks
- Actor/Trigger: Follow-up implementer executes the ordered one-week plan
- Expected Outcome: Each day has an executable product check, ending with offline CLI and MCP reads for one issue and one wiki page
- Evidence Type: local command
- Freshness: current source/build/runtime evidence required
- Mock Policy: external_dependencies_only


## outcome-16
- Request Task: 16
- Role: `supporting_evidence`
- Request Item: Dogfood observations that should feed back into Triborg/Runa/design-agent configuration.
- Target Surface: Public-safe feedback artifact and validation command
- Actor/Trigger: Coordinator records dogfood feedback after running the slice
- Expected Outcome: Feedback captures friction and prompt/config improvements without private paths, secrets, or private tracker/wiki names
- Evidence Type: local command
- Freshness: current source/build/runtime evidence required
- Mock Policy: no_mocks
