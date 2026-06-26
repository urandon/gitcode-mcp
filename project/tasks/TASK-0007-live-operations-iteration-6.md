# TASK-0007: Live operations iteration 6

Status: ready

## Goal

Make `gitcode-mcp` usable for a real agent loop after iteration 5 by closing the operational gaps that still force shell fallbacks, manual GitCode UI work, or ambiguous diagnostics.

Iteration 6 is not a rewrite of iteration 5. Treat iteration 5's implemented code and design package as historical input, and use the smoke report as the new evidence source. Do not edit `ai/design_implementator_gitcode_mcp_live_api_iteration_5_run_plan.yaml`; it is the run input for an already completed implementation wave.

## Context

Primary evidence:

- `project/handoffs/2026-06-24-live-api-iteration-5-smoke.md`
- `project/tasks/TASK-0006-live-api-coverage-iteration-5.md`
- `project/research/gitcode-wiki-api-v5-repository-model-2026-06-23.md`
- `docs/cache-and-sync-model.md`
- `docs/repo-binding.md`
- `docs/secrets.md`
- `docs/operations/mcp-installation-and-secrets.md`

Known findings from the iteration 5 smoke:

- MCP exposes cache read tools, but not the lifecycle operations an agent needs to bootstrap or refresh live context. Missing areas include repo binding/status, live sync, index refresh, auth status, doctor/readiness, migration/cache setup diagnostics, and cache path errors.
- `tools/list` can disappear when MCP startup cannot open or lock the cache path, so users see "no GitCode tools" instead of a useful readiness diagnostic.
- Large collection sync is not operationally safe. Wiki stress data showed long no-progress behavior and command timeout did not bound the full operation. Treat this as a generic collection problem for issues, wiki pages, comments, pull requests, labels, and milestones.
- Empty wiki initialization is still unresolved. The v5 wiki-as-repository path works after a wiki exists, but first-page creation failed until the operator manually created a page in the GitCode UI.
- After manual wiki initialization, `sync --wiki` worked but normalized `Home.md` to `wiki/Home.md.md`.
- `create-page` after manual wiki initialization reached the provider but failed write-confirmation decoding because the response did not contain the expected path/sha shape.
- Issue create/update can still fail because empty labels are serialized as `labels: []` even when the user did not request label mutation.
- `add-comment --live` has two live-path gaps: response decoding/`http_attempted` diagnostics, and credential resolution parity. In one smoke, `auth status` saw the Keychain token while `add-comment` reported missing live credentials; direct `/api/v5` curl with the same token succeeded.
- Pull request list/detail/comments routes have live evidence through the `/api/v5/repos/{owner}/{repo}/pulls` family and should no longer be deferred for lack of route evidence.
- Parallel read-style commands against one cache path can hit writer lock contention and surface as `internal_error`, which is risky for agent-side parallel MCP calls.

## Scope

1. Design the MCP lifecycle/control-plane surface needed for agents to operate without shell fallback.
2. Define startup/readiness diagnostics for cache path, schema, and lock failures so MCP does not silently vanish from clients.
3. Design bounded collection sync with progress/partial state/timeout semantics that apply across all collection types.
4. Resolve empty-wiki behavior: first-page bootstrap through a proven route, or a typed empty/uninitialized wiki diagnostic with concrete remediation.
5. Fix initialized-wiki path normalization and write confirmation handling.
6. Fix write credential parity so `auth status`, sync/read probes, and write commands use one credential resolver behavior.
7. Fix issue write payload omission for labels and live comment response decoding.
8. Bring PR read/comment routes into the route/schema/cache design using sanitized mocked evidence.
9. Decide whether MCP write tools stay unsupported in iteration 6; if so, known write names must return explicit `unsupported_capability` rather than appearing missing or performing credential lookup.

## Required Design Questions

- Which lifecycle operations must be exposed as MCP tools now: repo list/status/bind, live sync, index refresh, auth status, doctor/readiness, migrate-cache, cache reset, or a smaller curated set?
- Should live sync be one MCP tool with booleans for issues/wiki/index, or separate tools such as `sync_issues`, `sync_wiki`, and `index_repo`?
- How should MCP report startup failures when the cache is unwritable, schema-incompatible, or locked before normal tools can initialize?
- What is the empty-wiki product behavior: API bootstrap, manual UI remediation, optional git/SSH bootstrap, or explicit unsupported state?
- How should wiki paths be normalized so remote `.md` paths remain stable and do not become `.md.md`?
- What response shapes are valid for wiki create/update and issue/PR comments, and what confirmation evidence is required before cache/audit writes are marked successful?
- Which PR records and comment records should enter the cache, and how do they relate to issue-like records and source comments?
- What lock strategy lets concurrent read/MCP calls proceed safely while preserving writer integrity?

## Required Tests

Offline tests remain the primary acceptance gate.

Add mocked or fixture-backed tests for:

- MCP lifecycle bootstrap from an empty writable cache: repo binding/status, live or mocked sync, index refresh, then search/list over the refreshed cache.
- MCP startup/readiness diagnostics for unwritable cache path, incompatible schema, and writer-lock contention.
- Collection sync cancellation/progress behavior using a mocked paginated provider with enough records to prove bounded traversal.
- Empty/uninitialized wiki sync and first-page create behavior.
- Initialized wiki `.md` path normalization and create/update confirmation decoding.
- Keychain-equivalent credential parity across `auth status`, read/sync probes, and write commands.
- Issue create/update where labels are omitted unless the user explicitly requests label mutation.
- Comment write/read response decoding with accurate `http_attempted` diagnostics.
- PR list/detail/comment routes through sanitized `/api/v5/repos/{owner}/{repo}/pulls` shaped fixtures.
- Parallel MCP/read calls against one cache path returning stable results or typed retryable cache-busy diagnostics, not `internal_error`.

## Acceptance

- `go test ./...` passes without real GitCode credentials, network, SSH agent, browser cookies, or OS Keychain access.
- `git diff --check` passes.
- Agents can bootstrap and refresh live cached context through the designed MCP lifecycle path, or receive explicit unsupported/readiness diagnostics that explain the CLI-only boundary.
- Empty wiki behavior is explicit and tested.
- Initialized wiki sync does not generate `.md.md` paths.
- Wiki/page/comment write confirmation failures are classified accurately and do not imply successful cache confirmation.
- Write commands use the same credential resolution contract as `auth status`.
- Large collection sync is bounded, cancellable, and reports progress or partial state.
- PR read/comment route behavior is either implemented with tests or deliberately deferred with a route-specific diagnostic.
- Historical iteration 5 run plans remain unchanged.

## Out of Scope

- Mandatory live network tests.
- Browser-session cookies or browser-derived tokens as supported MCP credentials.
- Broad cache schema rewrite beyond targeted metadata/diagnostic changes.
- Requiring SSH keys for default MCP operation unless explicitly scoped as an optional wiki bootstrap provider.

## Validation Commands

```sh
go test ./...
git diff --check
```

Optional credential-gated smoke:

```sh
gitcode-mcp doctor --repo <repo>
gitcode-mcp sync --live --repo <repo> --issues --wiki --index --format json
gitcode-mcp list --repo <repo> --format json
```
