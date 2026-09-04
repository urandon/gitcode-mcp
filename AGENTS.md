# Agent Guide

This repository is the implementation home for cache-first GitCode MCP/CLI
tooling.

Mission: make GitCode workable for agents and humans under poor network
availability by keeping a durable local cache, deterministic exports, stable
identity resolution, and MCP access over cached data.

## Non-Negotiable Invariants

- Routine reads are cache-first and must not require live GitCode access.
- Remote writes are explicit, idempotency-gated, provider-confirmed, audited,
  and never hidden inside a read or maintenance path.
- Stable source ids such as `DOC-123` remain authoritative. Provider ids and
  URLs are aliases, not replacements.
- Git is the only durable store for repository-document bytes. The cache may
  persist metadata, locators, digests, and vectors, but not copies of README or
  `docs/` content.
- Credentials, cookies, authorization headers, private coordinates, local
  filesystem paths, and raw provider bodies never enter tracked files, issue or
  PR text, logs, fixtures, snapshots, screenshots, or test output.
- A red or unknown required check is a stop condition, not evidence that work
  is complete.
- Do not close an issue, merge a PR, or create a release tag while required
  review, exact-SHA CI, or release evidence is missing.

## Repository Boundary

Keep this repository self-contained and public-safe.

Use this repo for:

- code, tests, fixtures, cache schema, CLI, MCP, coordinator, and Admin UI work;
- durable GitCode API compatibility notes and sanitized captured fixtures;
- technical documentation that is part of the product surface.

Use GitCode issues and pull requests for active planning and handoffs. Use the
GitCode wiki for historical research, decisions, and dogfood evidence that
should remain discoverable without living in `main`.

Do not reference non-public source repositories, trackers, wiki names, raw
credentials, cookies, internal URLs, or unsanitized API responses. Source
systems should appear here only as generic concepts or sanitized fixtures.

## Work Authority And Tracking

- Every non-trivial change starts from an existing GitCode issue. Search for a
  duplicate before creating one.
- Treat an issue title and description as fixed task input. Do not edit either
  unless the user explicitly requests that exact metadata change.
- Publish design proposals, decisions, progress, verification evidence,
  independent-review results, handoffs, and later corrections as issue
  comments so the original input remains reviewable.
- Use one branch per issue, normally `codex/issue-<number>-<slug>`, based on the
  latest intended base branch. Reference the issue in the PR.
- When work belongs to an epic, record newly discovered tasks and dependency
  changes on the epic before treating the child issue as complete.
- Preserve user-owned or unrelated work in a dirty worktree. If a required edit
  overlaps changes whose ownership is unclear, stop and request direction.

## Read First

1. `README.md`
2. `docs/architecture.md`
3. `docs/component-architecture.md`
4. `docs/test-architecture.md`
5. `docs/cache-and-sync-model.md`
6. `docs/gitcode-api-discovery.md`
7. `docs/mcp-setup.md`
8. the linked GitCode issue and its design/progress comments
9. the linked pull request and unresolved review threads, when one exists

Read additional component documents only when the touched surface routes to
them. Do not implement from an issue summary while ignoring its authoritative
body or later correction comments.

## Autonomous Delivery Protocol

### 1. Recover Current State

- Inspect `git status`, current branch, base divergence, recent commits, linked
  issue/PR state, unresolved review threads, and the latest CI/release runs.
- Distinguish user changes from agent changes before editing.
- If resuming a failed run, identify the exact failing SHA and failure cause;
  do not assume the current checkout matches remote state.

### 2. Frame The Issue

- Reproduce or inspect the current behavior before proposing a fix.
- Record scope, acceptance criteria, non-goals, risks, dependencies, UX cohort
  impact, and a verification plan in an issue comment.
- For defects, capture a minimal deterministic regression test or fixture.
- For design work, publish diagrams and contracts before decomposition when the
  design controls multiple implementation tasks.

### 3. Implement In Reviewable Increments

- Prefer the smallest coherent change that satisfies the issue contract.
- Keep business behavior in shared service layers; CLI, MCP, and Admin are
  transports over those contracts.
- Keep live API quirks inside `internal/gitcode/` with sanitized compatibility
  evidence and deterministic offline tests.
- Add or update tests with the behavior. Do not postpone a required UI cohort,
  recovery path, migration, or observability surface without tracking it.
- Comment meaningful progress and discovered scope changes on the issue.

### 4. Verify Locally

Run focused tests while iterating, then the full applicable gates. The minimum
repository gate is:

```sh
go test ./...
git diff --check
```

For touched concurrent cache/service code, run the relevant race packages. For
Admin UI changes, also run the checked-in asset validation, Svelte checks, unit
tests, and Playwright semantic suite described in `docs/test-architecture.md`.
For release tooling, run its Python fixture tests. If a required live E2E test
cannot run, record the precise unavailable prerequisite and preserve an offline
fixture boundary; do not report it as passed.

### 5. Obtain Independent Exact-SHA Review

- Push a clean commit and give an independent reviewer the full commit SHA,
  issue contract, and known risks.
- The reviewer must inspect the code and tests independently, not merely accept
  the author's summary.
- Resolve every P0-P2 finding and request another review of the new exact SHA.
- Post the final finding list or explicit no-findings verdict on the issue. The
  author cannot substitute self-review for this gate.

### 6. Open And Validate The PR

- Open a PR from the issue branch to the intended base with scope, design,
  tests, risk, recovery, UX assessment, and `Closes #<issue>` metadata.
- Verify that the remote branch SHA equals the reviewed SHA.
- Observe required CI to completion for that exact SHA. A run on an earlier
  commit, another branch, or only a local checkout is not sufficient.
- Fix failures in the issue branch, rerun local gates, obtain review for the new
  SHA, and observe CI again. Never merge around a red required check.

### 7. Merge And Close

- Merge only after independent review is clear, required exact-SHA CI is green,
  the PR has no unresolved blocking thread, and the branch still targets the
  intended base.
- Observe the resulting `main` CI. If merge changes or integration failures
  make `main` red, reopen or create a blocking issue and fix forward before any
  release.
- Close the issue only when acceptance criteria and tracked child tasks are
  complete. Post the merge SHA and final evidence.

### 8. Release

- Release only when explicitly requested or required by the active issue.
- Tag a clean, already merged, green `main`; never tag an unmerged issue branch.
- Select the next SemVer tag from existing tags and the actual change scope.
- Run the gates in `docs/release-process.md`, create one immutable tag, push it,
  and observe both tag CI and the release workflow to terminal success.
- Verify tag commit, release notes, archive inventory, checksums, and reported
  binary version. Never move or reuse a published tag. A failed or ambiguous
  publisher leaves the release task open for fix-forward recovery.

## Session Handoff Safepoint

- When a session must be restarted, the default safepoint is the boundary of
  the current issue: implementation complete, exact-SHA review clear, PR and
  required CI green, merged, resulting `main` CI green, and the issue closed
  with final evidence. Do not begin the next issue before handing off.
- If an external blocker prevents reaching that boundary, leave a clean pushed
  branch and record the exact SHA, remaining findings, failed or pending gates,
  reproduction commands, and next action in an issue comment. Never represent
  this fallback handoff as a completed issue.
- A resumed session starts from the issue/PR record and remote exact SHA, then
  re-runs the recovery checks in step 1 before editing.

## UX Cohort Rule

When a proposal introduces a new user-visible resource, state, action,
lifecycle, query scope, or remediation path, treat it as a UX cohort. The
design and final decomposition must assess every supported product surface,
including the embedded Admin UI.

For a user-facing cohort, create an explicit UI issue or task. It may be
capability-gated or sequenced after backend work, but its dependencies and
rollout gate must remain visible. If no UI work is required, record the reason
in the issue comments instead of silently omitting that surface.

## CI And Evidence Contract

- Prefer machine-readable invariants: exact text, DOM roles, ARIA state,
  structured JSON/API fields, state transitions, deterministic exports, and
  stable links.
- Browser CI is semantic-only. Screenshots, video, Playwright traces, raster
  baselines, and pixel comparisons are opt-in local QA and must be disabled in
  CI. The artifact guard must fail if such output appears.
- Default tests are deterministic, offline, credential-free, and independent
  of Keychain, SSH agent, external DNS, and machine-local paths.
- Live compatibility evidence must be bounded, sanitized, and captured behind
  adapter fixtures. Never weaken a deterministic gate merely to make CI green.
- Evidence names the exact SHA and command. Distinguish `passed`, `skipped`,
  `not run`, and `blocked`; do not collapse them into “green”.

## Engineering Defaults

- Treat GitCode live API behavior as an adapter detail behind captured
  compatibility evidence.
- Prefer deterministic exports and sync logs so changes can be reviewed.
- Gate writes behind explicit commands and idempotency keys.
- Use bounded network, memory, payload, retry, queue, and retention behavior.
- Preserve already fetched durable work across local writer contention; retries
  must not amplify provider traffic.
- Keep degraded or partial coverage visible without making unrelated cached
  reads unavailable.
- Use `rg`/`rg --files` for discovery and `apply_patch` for manual edits.

## Stop Conditions

Stop, record the blocker, and request direction when:

- the task requires authority outside the issue or the user's explicit scope;
- a destructive action, credential change, external publication, or ambiguous
  remote mutation lacks authorization;
- repository state contains overlapping unknown changes;
- the authoritative issue/PR requirements conflict or materially changed;
- a required reviewer, exact-SHA CI run, main CI run, or release run is missing,
  red, cancelled, or cannot be tied to the intended commit;
- public-safety cannot be established for a fixture, log, URL, or report;
- the same external blocker persists after bounded safe alternatives are
  exhausted.

Do not “complete” an issue by deleting a failing test, weakening an invariant,
silently reducing scope, bypassing review, force-moving a tag, or declaring an
unobserved external action successful.

## Delivery Checklists

### Before Editing

- [ ] Issue exists, duplicate search is complete, and scope is understood.
- [ ] Required docs, issue comments, and PR threads are read.
- [ ] Worktree ownership and intended base branch are known.
- [ ] Design, UX-cohort impact, risks, and test plan are recorded.

### Before Commit

- [ ] Behavior and regression tests are implemented together.
- [ ] Public-safety and stable-identity invariants are preserved.
- [ ] CLI/MCP/Admin and docs impact is assessed.
- [ ] Focused tests, `go test ./...`, and `git diff --check` pass as applicable.
- [ ] Documentation changes pass `./scripts/check-product-docs.sh`; changed
      Mermaid diagrams are also previewed in a Mermaid-capable renderer.
- [ ] No generated secrets, caches, logs, screenshots, or ad hoc evidence are
      tracked.

### Before PR Or Merge

- [ ] Progress and exact verification evidence are posted to the issue.
- [ ] Independent reviewer checked the exact SHA; no P0-P2 finding remains.
- [ ] PR describes scope, risk, recovery, UX, and test evidence.
- [ ] Remote PR SHA matches the reviewed SHA and required exact-SHA CI is green.
- [ ] No unresolved blocking thread or untracked epic dependency remains.

### Before Release

- [ ] Change is merged and `main` CI is green.
- [ ] Release gates and notes tests pass on the tagged commit.
- [ ] SemVer is correct and the tag does not already exist.
- [ ] Tag CI and release workflow finish successfully.
- [ ] Release commit, assets, checksums, notes, and binary version are verified.

## Code Layout

- `cmd/gitcode-mcp/`: CLI entrypoint.
- `internal/`: package code, unit tests, offline integration tests, and explicit
  live E2E tests.
- `web/`: embedded Admin UI source and semantic browser tests.
- `docs/`: durable product, architecture, operations, and API documentation.
- `testdata/`: sanitized reusable fixture inputs.
