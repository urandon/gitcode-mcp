# TASK-0006: Live API coverage iteration 5

Status: ready

## Goal

Turn the live GitCode provider from "issue paths reached the real service in smoke tests" into a credible live surface for day-to-day agent use by closing the API-shape gaps discovered in the test polygon smoke.

Iteration 5 should be evidence-led. Do not extend mocked GitHub-like assumptions. Use documented GitCode OpenAPI where it exists, and use sanitized live probes only where public docs are missing or incomplete.

## Context

Primary evidence:

- `project/handoffs/2026-06-23-live-provider-iteration-4-polygon-smoke.md`
- `project/research/gitcode-wiki-api-v5-repository-model-2026-06-23.md`
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
- Browser network evidence shows wiki page-detail read traffic through `GET https://web-api.gitcode.com/api/v2/projects/wiki/detail`, wiki tree/list traffic through `GET https://web-api.gitcode.com/api/v2/projects/{owner}%2F{repo}.wiki/repository/file_list`, create traffic through `POST https://web-api.gitcode.com/api/v2/projects/wiki/create`, update traffic through `PUT https://web-api.gitcode.com/api/v2/projects/wiki/update`, and delete traffic through `DELETE https://web-api.gitcode.com/api/v2/projects/wiki/delete`; detail query parameters include `repo_path` and `file_path`; file-list query parameters include `repoId` for the encoded `{owner}/{repo}.wiki` repository and `ref_name`; create payload includes `repo_path`, `name`, `file_path`, `commit_message`, `content`, and `currUserId`; update payload includes `repo_path`, `name`, `file_path`, `commit_message`, and `content`; delete payload includes `repo_path` and `file_path`. These routes are not yet proven stable/public API and must be treated as discovery evidence only.
- Follow-up token-only smoke showed the configured keychain/PAT token is valid for the `/api/v5` live provider but is not accepted as a standalone credential for the observed `web-api.gitcode.com` wiki routes: `detail` and `file_list` returned 403 without cookies, `create` returned `TOKEN_INVALID_ERROR`, browser-like non-cookie headers did not help, and alternate token header/query placements did not make `detail` pass.
- Browser network evidence also shows `GET https://web-api.gitcode.com/uc/api/v1/user/oauth/token` as a likely session-cookie token bridge. A no-cookie smoke, and a smoke with only the configured keychain token as `GITCODE_ACCESS_TOKEN` cookie, both returned `200` with an empty body, so this route is not currently a proven MCP credential path.
- New `/api/v5` wiki-as-repository smoke found a stronger token-compatible read/list path: `GET /api/v5/repos/{owner}/{repo}.wiki/contents`, `GET /api/v5/repos/{owner}/{repo}.wiki/contents/{path}`, and `GET /api/v5/repos/{owner}/{repo}.wiki/raw/{path}` work for both a public GitCode docs wiki and the private test wiki. Directory listing returns file/dir arrays, `contents/{path}` returns base64 file JSON, and `raw/{path}` returns markdown. The configured keychain token worked with `Authorization: Bearer`, `private-token`, and `access_token` query placement.
- Full `/api/v5` wiki-as-repository CRUD smoke on a throwaway private wiki page succeeded: `POST contents/{path}` creates, `GET raw/{path}` reads markdown, `GET contents/{path}` returns `sha`, `PUT contents/{path}` updates with current `sha`, and `DELETE contents/{path}` deletes with current `sha`. Create/update `content` must be base64-encoded; plain markdown content produced an empty blob.
- SSH clone of the wiki repo works for the operator.
- The current token works for normal HTTPS git repo access and `/api/v5` wiki-as-repository reads, but not for HTTPS wiki git access.
- GitCode official API docs cover Issues, Pull Requests, Labels, Milestone, and OAuth overview pages under `https://docs.gitcode.com/docs/apis/`. GitCode official wiki product docs at `https://docs.gitcode.com/en/docs/help/home/org_project/wiki/wiki-intro` describe wiki behavior, page conventions, supported renderable formats, and clone behavior, but no Wiki REST namespace or documentation for the observed browser `web-api` wiki/OAuth-token routes was found. GitCode-owned source references are available at `https://gitcode.com/GitCode/GitCode-Docs` and `https://gitcode.com/GitCode/gitcode-skills`; a shallow read showed `GitCode-Docs` links to wiki pages that are not present in the ordinary repo clone, and `gitcode-skills` documents `/api/v5`, token validation, and file-content APIs but no wiki CRUD/list API. GitCode's privacy policy is compliance context for any browser-session/cookie approach, not an API contract.
- Pull requests, milestones, and comments are not yet covered as live surfaces.
- Cache provenance still allows fixture and live records to appear in the same repo namespace.

## Scope

1. Define a live API route and schema matrix for issues, labels, milestones, PRs, comments, and wiki.
2. Specify and implement issue/label request and response models using documented or observed GitCode shapes.
3. Add or correct live adapter methods for labels and milestones.
4. Add live read/sync coverage for PRs and comments if the documented routes are sufficient.
5. Decide the wiki strategy:
   - prefer the token-compatible `/api/v5/repos/{owner}/{repo}.wiki/contents|raw` route if mocked tests can cover directory traversal, file decoding, path encoding, create/update/delete base64 content semantics, `sha` handling, and error handling;
   - browser `web-api.gitcode.com/api/v2/projects/wiki/*` route only as fallback discovery unless a non-cookie credential path can be proven stable, token-compatible, and public-safe enough for MCP use;
   - otherwise git-backed wiki provider with an explicit non-goal or separate credential story for SSH/git auth.
6. Improve error classification for 400/schema/decode failures so they do not look like network outages.
7. Add cache provenance or an operator-safe live-cache reset/isolation story.
8. Decide whether write tools belong in MCP now, or keep MCP read-only and document CLI as the write surface.

## Required Design Questions

- Which GitCode OpenAPI routes are authoritative for labels, milestones, pull requests, and comments?
- Which of the official `/api/v5` docs pages should be treated as implementation authority, and which live-probed `web-api` findings must remain discovery-only because no docs were found?
- What exact request shape should `create-issue --labels` and `update-issue --labels` use?
- What exact response model should normalize label objects into cache source labels?
- Should `add-label` be removed, re-routed to documented label APIs, or implemented as issue update with string labels?
- What milestone fields need to enter cache records, if any, versus staying as issue metadata?
- Are PRs modeled as a distinct source kind, issue-like records, or out of scope for this iteration?
- Are issue comments and PR comments stored as child records, source body appendices, or cache comments only?
- Is wiki sync viable through `/api/v5/repos/{owner}/{repo}.wiki/contents|raw` using the current token model, and what path filtering/format filtering rules should decide which wiki files become cache records?
- Are the observed `web-api.gitcode.com/api/v2/projects/wiki/detail`, `/create`, `/update`, `/delete`, and wiki repository `/repository/file_list` routes usable outside the browser session with an MCP-appropriate credential? The current keychain/PAT token-only smoke failed, so any design using these routes must explain the credential model rather than assuming `/api/v5` auth carries over.
- Can the observed `web-api.gitcode.com/uc/api/v1/user/oauth/token` route be used without browser cookies, or is it explicitly out of scope because it requires browser session credentials?
- Which wiki write operations should iteration 5 expose now that `/api/v5/repos/{owner}/{repo}.wiki/contents/{path}` create/update/delete has passed live smoke, and how should idempotency keys, commit messages, current `sha`, and base64 content encoding be modeled?
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
