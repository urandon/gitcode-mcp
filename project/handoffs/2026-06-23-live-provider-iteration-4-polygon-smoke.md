# Handoff: live-provider iteration 4 polygon smoke

Task: `TASK-0005-live-provider-wiring-iteration-4`

Branch checked: `feat/live-provider-wiring-iteration-4`

Commit checked: `c160dc3`

Smoke time: `2026-06-23T00:58:33Z`

## Context

Iteration 4 was smoke-tested against a dedicated GitCode test repository after the operator installed the current branch with `go install ./...`.

This pass exercised:

- Keychain-backed credentials with `GITCODE_TOKEN` unset;
- repository binding with `issues,wiki` scopes and `https://api.gitcode.com/api/v5`;
- live issue creation through the CLI;
- live issue/wiki sync into the default local cache;
- cache-first listing after live sync;
- `doctor` and `sync-status` operator diagnostics.

Repository owner/name, raw API headers, cookies, trace ids, and token material are intentionally omitted.

## Summary

Live issue read/write is now meaningfully working through the installed CLI and Keychain credential path.

The live smoke is still not fully green:

- real GitCode issue responses differ from the mocked schema and need tolerant decoding;
- live wiki sync fails because the current wiki list endpoint returns `404 Not Found`;
- live sync can leave old fixture records in the same repo namespace;
- `doctor` without `--live` has stale live-provider remediation text;
- `sync-status` reports cached-record freshness but does not clearly expose the latest partial sync failure.

## Commands and observations

### Repository binding

The operator bound the test repository with:

```sh
gitcode-mcp repo add \
  --repo <owner>/<repo> \
  --owner <owner> \
  --name <repo> \
  --api-base-url https://api.gitcode.com/api/v5 \
  --scopes issues,wiki \
  --alias testing-polygon
```

Result: passed.

The binding reports:

- repo id: `<owner>/<repo>`;
- API base URL: `https://api.gitcode.com/api/v5`;
- scopes: `issues,wiki`;
- alias: `testing-polygon`.

### Credential path

With `GITCODE_TOKEN` unset, the operator ran:

```sh
gitcode-mcp auth status --format json
```

Result: passed.

Observed:

- `source: keychain`;
- `present: true`;
- `store_mode: auto`;
- available sources include `keychain` and `env:GITCODE_TOKEN`.

This confirms the native Keychain resolver is active for the installed CLI.

### Live issue create

The operator ran:

```sh
gitcode-mcp create-issue --live \
  --repo <owner>/<repo> \
  --title "manual keychain smoke" \
  --body "manual smoke"
```

Initial result: failed after reaching the live provider.

Observed failure:

```text
live_api_failure: live provider returned an API failure
gitcode: partial response for /api/v5/repos/<owner>/<repo>/issues: malformed JSON
http_attempted: true
```

A manual API request to the same create-issue route succeeded and returned a valid JSON body. The important sanitized response-shape difference was:

```json
{
  "id": 4109571,
  "number": "3",
  "state": "open",
  "title": "manual curl smoke"
}
```

Current adapter models had assumed a narrower mocked shape. A local experimental decoder patch was tried during the smoke to validate the hypothesis; it is not part of the iteration-4 baseline. That experiment was moved to branch `feat/live-response-decoder-experiment` and covered:

- `internal/gitcode/models.go`;
- `internal/gitcode/client_test.go`.

Validation run for the experimental patch:

```sh
go test -count=1 ./internal/gitcode
go test -count=1 ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider' -v
go test -count=1 ./internal/service
go test -count=1 ./...
git diff --check
```

Result: passed.

### Live sync

The operator ran:

```sh
gitcode-mcp sync --live \
  --repo <owner>/<repo> \
  --issues \
  --wiki \
  --index \
  --format json
```

Result: partial success.

Observed:

- `success_count: 4`;
- `failure_count: 1`;
- issue records were fetched/updated/inserted;
- wiki bulk sync failed at `wiki:*`.

The failure was:

```text
gitcode: api_response: status=Not Found body=[REDACTED] at /api/v5/repos/<owner>/<repo>/wiki
```

Implication: the current live wiki list route shape is not accepted by real GitCode for this repository/API surface. The mocked route `/api/v5/repos/{owner}/{repo}/wiki` is insufficient evidence for live wiki support.

### Cache-first list after live sync

The operator ran:

```sh
gitcode-mcp list --repo <owner>/<repo>
```

Result: passed.

Observed:

- real issue records appeared in the local cache after live sync;
- cached issue ids use stable source ids such as `ISSUE-4109578`;
- records are listed from cache, not directly from live GitCode.

Observed caveat:

- old fixture records such as `ISSUE-42` and `WIKI-HOME` can remain in the same repo namespace after live sync;
- live sync did not add new fixture records, but it also did not isolate or purge previous fixture seed data.

This is a cache hygiene/product UX gap for transitioning a repo from fixture/offline seed data to live data.

### Doctor

The operator ran:

```sh
gitcode-mcp doctor
```

Observed:

- credential status correctly reported a Keychain token;
- repo/cache/index/MCP sections were available;
- `live_provider` reported `status: skipped`, `provider_mode: fixture`.

That offline default is expected for `doctor` without `--live`.

Observed UX gap:

```text
remediation: set GITCODE_TOKEN and use --live to enable live provider
```

This remediation is stale now that Keychain is a supported credential source. It should instead tell the operator to run `doctor --live` for live-readiness evaluation, or mention `GITCODE_TOKEN` and Keychain as equivalent credential options.

The live doctor path was checked with:

```sh
gitcode-mcp doctor --live --format json
```

Result: passed.

Observed:

- `credential.source: keychain`;
- `credential.token_present: true`;
- `live_provider.status: configured`;
- `live_provider.provider_mode: live-http`;
- `live_provider.api_base_url: https://api.gitcode.com/api/v5`;
- `live_provider.api_base_url_source: repository_binding.api_base_url`;
- `live_provider.readiness_status: ready`.

### Sync status

The plain doctor output showed:

- `sync.status: available`;
- `fresh_count: 6`;
- `stale_count: 0`;
- `cache_empty: false`;
- `zero_delta: true`.

This is accurate as cached-record freshness, but it is not sufficient operator feedback after a partial live sync. The latest live sync had a wiki scope failure, while the status surface can still look green because existing cached records are fresh.

Recommended product distinction:

- `sync-status` should continue to report cached-record freshness;
- it should also expose last sync health, failed scopes, and partial failures such as `wiki:*` route 404.

## Findings

### P1: live wiki route is not proven against real GitCode

Current route:

```text
GET /api/v5/repos/{owner}/{repo}/wiki
```

Real result:

```text
404 Not Found
```

Impact:

- `sync --live --wiki` cannot ingest real wiki pages from the test repository;
- MCP/cache-first wiki reads remain fixture-only or stale unless a real route is discovered.

Next action:

- discover the documented or observed GitCode wiki list/get route;
- add a sanitized live-shaped fixture or mock route that matches the real response;
- update endpoint builders and contract tests.

Follow-up discovery from the GitCode UI:

- the wiki page is exposed in the browser at `/wiki/Home.md`;
- the UI advertises a local clone URL shaped like `https://gitcode.com/{owner}/{repo}.wiki.git`;
- this strongly suggests GitCode wiki content is git-backed rather than exposed through the currently assumed REST route.

Additional live probes:

- unauthenticated `git ls-remote https://gitcode.com/{owner}/{repo}.wiki.git` failed with project read denial;
- `git ls-remote` with the existing Keychain REST token as a Bearer header failed with Git Basic auth denial;
- `git ls-remote` with the token through Basic/PAT-style credential helper variants still failed with project read denial;
- checked GitHub/GitLab/Gitee-like HTTPS auth variants for the wiki repo:
  - username `x-access-token`, password token;
  - username `oauth2`, password token;
  - username `git`, password token;
  - username real login, password token;
  - `PRIVATE-TOKEN`, `token`, and `x-auth-token` headers;
  - token-as-username variant;
  - alternate HTTPS wiki paths without `.git`, `/wiki.git`, and `/_wiki.git`;
- all wiki HTTPS git variants failed;
- the same token as Basic password with the real login succeeded for the normal repository `https://gitcode.com/{owner}/{repo}.git`;
- `curl -I https://gitcode.com/{owner}/{repo}/wiki/Home.md` returned an HTML page, not raw markdown content.

Documentation discovery:

- GitCode's older public OpenAPI index lists repository APIs for Branch, Commit, Tag, Issues, Pull Requests, Labels, Milestone, Release, Webhooks, and Member, but no Wiki namespace was found.
- GitCode's newer API docs expose the `/api/v5` base and issues routes, but no useful wiki page/list route or response schema was found.
- Chinese-language searches for `GitCode wiki API 文档`, `GitCode Wiki 克隆 .wiki.git token`, `GitCode 项目 Wiki 接口`, `GitCode 项目Wiki`, `GitCode WikiFiles`, and related variants did not reveal a documented REST/wiki API or token-compatible wiki clone recipe.
- GitHub uses a similar git-backed wiki model: `https://github.com/{owner}/{repo}.wiki.git`, with wiki edits performed through UI or git clone/push rather than a first-class wiki-pages REST API.
- The important difference from GitHub is that GitHub PAT-style HTTPS git access normally works for `repo.wiki.git`, while GitCode live probes showed the current token works for the normal repo git endpoint but not for the wiki git endpoint.

Implication:

- wiki live support should be redesigned as either a git-backed wiki fetch path or a separate GitCode web/API discovery path;
- the current token is sufficient for normal HTTPS git access but not sufficient, not scoped, or not routed correctly for the wiki git repository;
- SSH clone of the wiki repo works for the operator, so the wiki data exists and the issue is specifically token-compatible non-interactive wiki access;
- the implementation needs an explicit wiki auth story before live wiki sync can be considered ready.

### P1: live response schema drift was not covered by mock tests

Real issue create returned:

- numeric `id`;
- string `number`.

Impact:

- live create reached the provider and succeeded remotely, but adapter decode failed locally;
- issue write/read path looked broken even though auth and HTTP were working.

Next action:

- keep tolerant issue id/number decoding;
- add sanitized live-shaped response fixtures for issue create/list/get;
- avoid treating mock-only JSON shape as the product contract.

### P2: live cache can contain old fixture records

Observed records included both real live issues and old fixture records in the same repo namespace.

Impact:

- `list` can overstate live coverage;
- agents may cite or reason over fixture records while the operator believes the repo is live-backed.

Next action:

- add cache provenance or provider marker to records;
- provide a safe command or sync mode to clear fixture records for a repo before live sync;
- make live smoke use a fresh cache or explicitly report mixed provenance.

### P2: `sync-status` does not expose last partial sync failure clearly enough

Observed doctor/sync freshness can report all cached records fresh while the latest live wiki sync failed.

Impact:

- operators can miss failed scopes after a partial live sync;
- green-looking status may hide that wiki ingestion is not working.

Next action:

- store/report last sync scope health separately from cached-record freshness;
- include failed aliases/scopes and failure classes in `sync-status --format json`;
- render partial-failure status in `doctor`.

### P3: non-live `doctor` remediation still names env-only credential setup

Observed:

```text
set GITCODE_TOKEN and use --live to enable live provider
```

Impact:

- confusing when Keychain is already configured and visible in the same report.

Next action:

- change remediation to point to `doctor --live`;
- mention Keychain/credential store alongside env token where credential setup is discussed.

## Current implementation state

The live issue response-shape decoder change is not assumed to be present in this branch. The experiment was moved to branch `feat/live-response-decoder-experiment`:

- `internal/gitcode/models.go`;
- `internal/gitcode/client_test.go`.

Those experimental changes passed:

```sh
go test -count=1 ./...
git diff --check
```

## Recommended next actions

1. Implement the tolerant issue decoder fix and its regression test in the next implementation slice, using the experiment on `feat/live-response-decoder-experiment` only as optional reference evidence.
2. Add this smoke report to the iteration 4 review bundle.
3. Discover/fix the real GitCode wiki list/get endpoint.
4. Add live-shaped sanitized fixtures for issue and wiki API responses.
5. Add cache provenance or live-cache reset guidance to avoid fixture/live mixing.
6. Improve `doctor` and `sync-status` wording/fields for partial live syncs.

## Post-restart MCP and write smoke

Follow-up smoke after restarting Codex and switching the MCP server config to the direct Go binary:

```toml
[mcp_servers.gitcode]
enabled = true
command = "/Users/urandon/go/bin/gitcode-mcp"
args = ["--mcp"]
```

Codex could see and call the connected `gitcode` MCP tools after restart.

MCP read checks passed:

- `cache_status` returned cache counts and WAL state;
- `list_sources` returned cached live issue records;
- `search_sources` found imported task records;
- `get_source` returned the edited issue body for a live-synced task issue;
- `sync_status` reported cached freshness.

The connected MCP surface is currently read/cache oriented. It exposes tools such as `cache_status`, `list_sources`, `search_sources`, `get_source`, `sync_status`, and chunk/snapshot helpers, but does not expose issue write tools. Live writes were therefore tested through the CLI and read back through MCP after live sync.

Live write checks passed:

- created a simple smoke issue: remote number `5`;
- created sanitized task-import issues:
  - `TASK-0176` -> remote number `6`;
  - `TASK-0167` -> remote number `7`;
  - `TASK-0136` -> remote number `8`;
- updated remote number `7` by editing body content;
- updated remote number `8` by editing title and body content;
- synced live issues into the Codex MCP cache with `sync --live --issues --index`;
- read the updated records back through MCP.

Observed label/write gap:

- `create-issue --labels ...` failed with `live_transport_failure`;
- `update-issue --labels ...` failed with `live_transport_failure`;
- `add-label --live` failed with `live_transport_failure` at `/api/v5/repos/{owner}/{repo}/issues/{number}/labels`.

Because ordinary live create/update and external connectivity worked in the same environment, this is likely a label route/payload/API compatibility problem or an error-classification problem, not a general network or credential failure.

Follow-up after labels were created in the GitCode project:

- `add-label --live` still failed at `/issues/{number}/labels`;
- manual `POST /issues/{number}/labels` with JSON `{"label":"enhancement"}` returned `400 Bad Request`;
- manual form-encoded `label=enhancement` and `labels=documentation` also returned `400 Bad Request`;
- manual `POST /issues` or `PATCH /issues/{number}` with JSON array `{"labels":["enhancement"]}` returned `400 Bad Request`;
- manual `POST /issues` and `PATCH /issues/{number}` with JSON string `{"labels":"enhancement"}` succeeded.

Observed live API shape:

- request payload wants `labels` as a string, not a JSON array;
- response payload returns `labels` as an array of objects containing at least `id`, `name`, `color`, `created_at`, and `updated_at`;
- current CLI/model code exposes `Labels []string`, so both write payload and response decode are incompatible with live labels.

Additional consequence:

- after a manual label update succeeded through curl, `sync --live --issues --index` failed on `/api/v5/repos/{owner}/{repo}/issues`;
- the visible error was reported as a partial response/configuration failure rather than a clear schema decode failure;
- this should become a label response-shape contract test and an error-classification fix.

Observed MCP/cache caveat:

- live issue sync into the Codex MCP cache succeeded with `success_count: 8` and `failure_count: 0` for `--issues`;
- old fixture records still remained in the same repo namespace, reinforcing the cache provenance/fixture cleanup gap above.
