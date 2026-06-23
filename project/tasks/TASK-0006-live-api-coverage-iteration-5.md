# TASK-0006: Live API coverage iteration 5

Status: ready

## Goal

Turn the live GitCode provider from "issue paths reached the real service in smoke tests" into a credible live surface for day-to-day agent use by closing the API-shape gaps discovered in the test polygon smoke.

Iteration 5 should be evidence-led. Do not extend mocked GitHub-like assumptions. Use documented GitCode OpenAPI where it exists, and use sanitized live probes only where public docs are missing or incomplete.

## Context

Primary evidence:

- `project/handoffs/2026-06-23-live-provider-iteration-4-polygon-smoke.md`
- `project/tasks/TASK-0005-live-provider-wiring-iteration-4.md`
- `docs/gitcode-api-discovery.md`
- `docs/live-readiness.md`

Known live findings:

- Issue create/update/sync can reach the real service through Keychain-backed live CLI paths, but response-shape compatibility is still implementation work.
- Connected MCP read tools work after syncing live issues into the Codex cache.
- Issue response fields are not GitHub-shaped: `id` can be numeric and `number` can be a string.
- Labels are not represented correctly:
  - create/update request payload accepts `labels` as a string, not `[]string`;
  - response payload returns label objects, not strings;
  - `add-label` route `/issues/{number}/labels` does not match observed GitCode behavior;
  - 400/schema failures are currently surfaced as confusing transport/configuration failures.
- Wiki is not available through the assumed REST route `/api/v5/repos/{owner}/{repo}/wiki`.
- GitCode UI exposes wiki as `/wiki/*.md` plus `{repo}.wiki.git`.
- SSH clone of the wiki repo works for the operator.
- The current token works for normal HTTPS git repo access but not for HTTPS wiki git access.
- GitCode OpenAPI covers Issues, Pull Requests, Labels, and Milestone, but no Wiki REST namespace was found.
- Pull requests, milestones, and comments are not yet covered as live surfaces.
- Cache provenance still allows fixture and live records to appear in the same repo namespace.

## Scope

1. Define a live API route and schema matrix for issues, labels, milestones, PRs, comments, and wiki.
2. Specify and implement issue/label request and response models using documented or observed GitCode shapes.
3. Add or correct live adapter methods for labels and milestones.
4. Add live read/sync coverage for PRs and comments if the documented routes are sufficient.
5. Decide the wiki strategy:
   - token-compatible raw/API route if one is found;
   - otherwise git-backed wiki provider with an explicit non-goal or separate credential story for SSH/git auth.
6. Improve error classification for 400/schema/decode failures so they do not look like network outages.
7. Add cache provenance or an operator-safe live-cache reset/isolation story.
8. Decide whether write tools belong in MCP now, or keep MCP read-only and document CLI as the write surface.

## Required Design Questions

- Which GitCode OpenAPI routes are authoritative for labels, milestones, pull requests, and comments?
- What exact request shape should `create-issue --labels` and `update-issue --labels` use?
- What exact response model should normalize label objects into cache source labels?
- Should `add-label` be removed, re-routed to documented label APIs, or implemented as issue update with string labels?
- What milestone fields need to enter cache records, if any, versus staying as issue metadata?
- Are PRs modeled as a distinct source kind, issue-like records, or out of scope for this iteration?
- Are issue comments and PR comments stored as child records, source body appendices, or cache comments only?
- Is wiki sync viable with the current token-only model?
- If wiki requires SSH/git credentials, is that acceptable for this product slice, or should wiki remain blocked with clear diagnostics?
- Should live commands default to the only bound repo, or should `--repo` remain mandatory for now?

## Required Tests

Offline tests must remain the primary acceptance gate.

Add mocked live-provider tests for:

- create issue with label string payload;
- update issue with label string payload;
- list/get issues whose labels arrive as objects;
- malformed request/400 response classified as API/schema/configuration failure, not network unavailable;
- documented label or milestone routes using sanitized fixture responses;
- PR/comment route tests if included in scope;
- wiki blocked diagnostics if no token-compatible route is found;
- fixture/live provenance or cache isolation behavior.

Optional live smoke may run against the dedicated testing polygon only when credentials are available. It must stay redacted and must not become the default test gate.

## Acceptance

- `go test ./...` passes without real GitCode credentials, network, SSH agent, or OS Keychain access.
- `git diff --check` passes.
- Label create/update paths work against mocked GitCode-shaped payloads.
- Label object responses decode and normalize into cache source labels.
- 400/decode/schema errors are reported with clear failure classes.
- Milestone routes are either implemented with tests or explicitly deferred with a documented reason.
- PR/comment routes are either implemented with tests or explicitly deferred with a documented reason.
- Wiki has an explicit design decision and operator diagnostic:
  - token-compatible route found and covered by fixtures; or
  - wiki live sync remains unsupported with a clear reason and next credential/discovery step.
- MCP write exposure is explicitly decided, not accidentally absent.
- A handoff records live smoke results and remaining gaps.

## Out of Scope

- Broad cache schema rewrite.
- Making live network tests mandatory.
- Storing raw API responses, cookies, tokens, private coordinates, or unsanitized browser captures.
- Requiring SSH keys for default MCP operation unless the design explicitly scopes a separate optional wiki-git provider.
- Replacing the cache-first read model.

## Validation Commands

```sh
go test ./...
git diff --check
```

Optional credential-gated smoke:

```sh
gitcode-mcp sync --live --repo <test-repo> --issues --index --format json
gitcode-mcp create-issue --live --repo <test-repo> --title "smoke" --body "smoke" --labels "enhancement" --format json
```
