# GitCode MCP Live API Iteration 5 Smoke Report

Date: 2026-06-24
Branch: `feat/live-api-coverage-iteration-5`
Target repository: `urandon/gitcode-mcp-testing-polygon`
Local test cache: `/tmp/gitcode-mcp-iter5-live-smoke-001.db`

## Summary

Iteration 5 improves live read normalization and live-cache visibility, but it is not ready for routine live GitCode use yet. The issue read path works against the test polygon and stores live records with stable local ids. The write path currently regresses for issue create/update and comment writes, and large collection sync needs a streaming/progress/timeout design that applies to every collection, not only wiki.

## What Passed

- `go test ./...` passed for all packages.
- `git diff --check` passed.
- `gitcode-mcp auth status --format json` on the installed binary saw the token from keychain.
- `sync --live --issues --repo urandon/gitcode-mcp-testing-polygon --format json` fetched 10 live issues successfully.
- `list`, `search`, `sync-status`, and positional `get` worked against the live issue cache.
- Live issue identities looked correct in the cache: `ISSUE-1` through `ISSUE-10`; no `ISSUE-0` identity regression was observed.
- Cached live records were surfaced as `provenance: live`.
- After moving the Codex MCP cache to a writable path and seeding it through the CLI, the MCP read tools were callable and could list/search live cached polygon issues.
- The private polygon wiki already had content, so previous wiki CRUD smoke only proves behavior for an initialized wiki repository.

## High Priority Gaps

### 0. Pull Request API is available but not wired into GitCode MCP

After the initial report, a read-only live probe found that GitCode exposes pull requests through the `/api/v5/repos/{owner}/{repo}/pulls` route family. The GitCode UI still names them merge requests, but the token-compatible API route is `pulls`.

Observed live routes:

```text
GET /api/v5/repos/urandon/gitcode-mcp-testing-polygon/pulls
GET /api/v5/repos/urandon/gitcode-mcp-testing-polygon/pulls/1
GET /api/v5/repos/urandon/gitcode-mcp-testing-polygon/pulls/1/comments
POST /api/v5/repos/urandon/gitcode-mcp-testing-polygon/pulls/1/comments
```

The following route aliases returned `404` and should not be used as implementation paths:

```text
/pull_requests
/merge_requests
/pulls/{number}/review_comments
/pulls/{number}/notes
/pulls/{number}/reviews
```

Live PR list/detail returned GitCode-shaped PR payloads with fields including `id`, `number`, `html_url`, `state`, `title`, `body`, `user`, `labels`, `base`, `head`, `mergeable`, and `mergeable_state`. A PR comment write on the test polygon returned `201` with:

```text
id: discussion id string
note_id: numeric comment id
body: comment body
```

Read-back through `GET /pulls/1/comments` returned a list with `id`, `discussion_id`, `body`, `created_at`, `updated_at`, `user`, and `comment_type: pr_comment`.

This changes the next-iteration recommendation: PR read and PR comments no longer need to remain deferred for lack of route evidence. They still need normal adapter modeling, fixture coverage, cache projection decisions, and public-safe diagnostics.

### 1. MCP tools are read-only and cannot operate the live lifecycle

The MCP server now exposes cache read tools, but it does not expose the operational tools needed to make a live cache useful from an agent session. The current callable MCP tool list includes search/list/get/snippet/index-health/snapshot operations, but not repository binding, live sync, indexing, migration, doctor/readiness, auth status, repo status, or write operations.

Current MCP tool list:

```text
search_sources
get_source
list_sources
list_chunks
search_chunks
get_snippet
stale_index_report
recent_changes
link_check
cache_status
source_backlinks
resolve_id
sync_status
export_snapshot
diff_snapshot
```

This means an agent cannot bootstrap or refresh its own cache through MCP. The cache had to be seeded with CLI commands (`repo add`, `sync --live`, `index --full`) before MCP reads became useful. That is acceptable as a temporary boundary only if documented explicitly; it is not enough for a day-to-day agent workflow where Codex should connect to a repository and refresh context without shell fallback.

Additional MCP-specific findings:

- MCP startup is sensitive to cache path write permissions because it opens the cache and writer lock before `tools/list`.
- `tools/list` works with a writable cache path, but a missing or unwritable cache path makes Codex show no GitCode tools at all.
- The MCP surface has no `repo_add`, `repo_status`, `sync_live`, `index`, `doctor`, `auth_status`, or `migrate_cache` equivalents.
- MCP has no write tools by design in iteration 5, but known write calls should return structured `unsupported_capability`; read-side lifecycle operations should not be missing silently.

Expected next behavior:

- Decide which CLI lifecycle commands must become MCP tools now: at minimum repo status/list/bind, cache status, live sync, index, doctor/readiness, and auth status.
- Keep dangerous mutations behind explicit capability boundaries, but expose safe live refresh/readiness operations to MCP.
- Make MCP startup degrade gracefully when cache setup fails, ideally with a small `tools/list` surface that can report `cache_path_unwritable` instead of disappearing from Codex.
- Add an MCP bootstrap smoke test: empty writable cache -> repo binding -> live or mocked sync -> index -> search/list via MCP.

### 2. Large collection sync is not operationally safe

The wiki stress case exposed a broader collection-size problem. `sync --live --wiki` against the polygon repository did not return useful progress and had to be interrupted after a long wait. Retrying with `--timeout 5s` did not bound the whole command.

This should be treated as a generic large-collection issue, not a wiki-only defect. Issues, comments, pull requests, wiki pages, milestones, labels, and future collections can all grow past the point where "fetch everything serially and answer at the end" is usable.

Implementation clues:

- `cmd/gitcode-mcp/main.go` routes CLI and MCP with `context.Background()`, so startup timeout is not acting as a command deadline.
- `internal/gitcode/http_client.go` wiki traversal fetches all pages recursively before returning a `Page[WikiPage]`.
- Wiki traversal has `seenDirs`, `seenFiles`, and a depth cap, so the observed behavior looks more like unbounded serial work than an obvious recursion loop.

Expected next behavior:

- Collection sync should have bounded paging/batching.
- Long syncs should emit progress or durable partial sync events.
- Command-level timeout/cancellation should stop traversal and return a clear retryable diagnostic.
- Partial progress should not leave the operator guessing whether the command is hung.

### 3. Empty wiki initialization is not supported by the current v5 wiki path

`urandon/gitcode-mcp` is the important empty-wiki case. The repository binding has `issues,wiki`, but the wiki repository is not initialized from the API point of view.

Observed live behavior:

- `sync --live --repo urandon/gitcode-mcp --wiki --format json` failed on `GET /api/v5/repos/urandon/gitcode-mcp.wiki/contents` with `400 Bad Request` classified as `api_validation`.
- `create-page --live --repo urandon/gitcode-mcp --slug codex-mcp-smoke-2026-06-24` failed on `POST /api/v5/repos/urandon/gitcode-mcp.wiki/contents/codex-mcp-smoke-2026-06-24` with `400 Bad Request`.
- `create-page --live --repo urandon/gitcode-mcp --slug Home` failed on `POST /contents/Home` with `400 Bad Request`.
- `create-page --live --repo urandon/gitcode-mcp --slug Home.md` failed on `POST /contents/Home.md` with `400 Bad Request`.
- `git ls-remote https://gitcode.com/urandon/gitcode-mcp.wiki.git` returned 403/project-not-found style output, consistent with the wiki repo not existing yet for HTTPS access.
- After the operator manually created one wiki page in the GitCode UI, `sync --live --repo urandon/gitcode-mcp --wiki --format json` succeeded and inserted one live wiki record.
- The inserted manual `Home.md` page normalized to `id: WIKI-HOME.MD`, `remote_alias: wiki:Home.md`, and path `wiki/Home.md.md`, so path extension handling is wrong for `.md` wiki files.
- After manual initialization, `create-page --live --repo urandon/gitcode-mcp --slug codex-mcp-smoke-2026-06-24` no longer returned the earlier 400. It reached the provider but failed confirmation decoding with `schema_decode`: `wiki write confirmation requires path and sha`. A follow-up sync still listed only `Home.md`, so create is not proven successful.

This means the validated `/api/v5/repos/{owner}/{repo}.wiki/contents` CRUD path is currently proven only for already-initialized wiki repositories, and even there the installed CLI still has a write-confirmation/schema gap. It does not prove first-page creation or reliable page creation from the product runtime.

Expected next behavior:

- Add an explicit empty-wiki state to the route/schema matrix and tests.
- Decide whether empty wiki initialization must use a GitCode product/browser route, a separate create-wiki endpoint, SSH/git bootstrap, or a documented unsupported diagnostic.
- `sync --wiki` on an empty wiki should not look like a generic provider failure. It should return a typed empty/uninitialized wiki diagnostic that tells the operator what to do next.
- `create-page` should either initialize the wiki and create the first page, or fail before misleadingly claiming the normal initialized-wiki CRUD path is enough.
- Fix wiki path normalization so a remote page path ending in `.md` does not become `wiki/<name>.md.md` locally.
- Fix initialized-wiki write confirmation decoding, or classify the unsupported response shape without implying success.

### 4. Live issue create/update currently returns GitCode 400

The installed iteration 5 binary returned `api_validation` for all tested issue write variants:

- `create-issue --live` without labels.
- `create-issue --live --labels bug,documentation`.
- `update-issue --live --title ...`.
- `update-issue --live --labels bug,documentation`.

The likely regression is label payload serialization:

- `gitcode.EncodeIssueLabels(nil)` returns `[]`.
- `service.callWriteAdapter` always passes `Labels: gitcode.EncodeIssueLabels(req.Labels)` for create/update.
- `json.RawMessage("[]")` is not omitted by `omitempty`, so even no-label create/update appears to send `"labels":[]`.

Expected next behavior:

- Omit `labels` entirely when the user did not request label mutation.
- Verify the exact GitCode payload accepted by live create/update issue endpoints.
- Keep the fixture tests, but add a live-compatible test case that prevents empty `labels: []` from being sent implicitly.

### 5. Comment write response handling is still not live-compatible

`add-comment --live` attempted the HTTP write, but returned a schema decode failure:

```text
schema_decode: response schema decode failure: ... partial response ... malformed JSON
```

The diagnostic payload also reported `http_attempted:false`, which is misleading for a live partial/schema decode after a write request.

Expected next behavior:

- Capture a sanitized fixture for the live comment response shape.
- Decode the real response shape or classify it as a provider compatibility gap with accurate `http_attempted:true`.
- Confirm whether the remote comment was created before deciding cache-confirmation behavior on decode failure.
- Fix credential resolution consistency for write commands. In this session `auth status --format json` reported `source: keychain` and `present: true`, but `add-comment --live --repo urandon/gitcode-mcp --number 1 ...` failed before HTTP with `config_credential` / missing `GITCODE_TOKEN or configured credential`. The issue was updated through direct `/api/v5/repos/urandon/gitcode-mcp/issues/1/comments` curl using the same Keychain token, so the credential itself was valid.

### 6. Cache lock contention appears under parallel reads

Two parallel read-style commands against the same cache path hit:

```text
cache: lock contention at /tmp/gitcode-mcp-iter5-live-smoke-001.db.writer.lock: another process holds the cache lock
```

This matters for MCP because agents commonly issue parallel tool calls. Even if a command performs a small startup write or status touch internally, read concurrency should not degrade into `internal_error`.

Expected next behavior:

- Separate read-only command paths from writer lock acquisition where possible.
- Classify lock contention as a specific retryable/cache-busy diagnostic instead of `internal_error`.
- Add an MCP-style concurrent read smoke test using one cache path.

## Lower Priority UX Gaps

- Root help advertises `mcp serve`, but `gitcode-mcp mcp --help` returns `unknown command "mcp"`.
- Root help lists `--id ID` as a global query flag, but `get` expects a positional id and rejects `get --id ISSUE-8`.
- `list --provenance live` / `list --provenance fixture` is not wired in the CLI, even though provenance is now visible in results.
- `add-label --dry-run` intentionally defers to `update-issue --labels`, but the error currently classifies as `internal_error` rather than an unsupported capability or configuration-level response.

## Live Evidence Snapshot

Successful issue sync produced 10 fresh live records:

```text
fresh_count: 10
stale_count: 0
cache_empty: false
provenance: live
```

Representative cached issue:

```text
id: ISSUE-8
remote_alias: gitcode_issue_id:4109607
kind: issue
title: [MCP smoke][edited] TASK-0136 MCP control-plane surface
status: open
provenance: live
```

Failed write examples all reached live mode with the configured repository binding and returned provider/API errors rather than missing credentials.

## Suggested Follow-up Issue

Title:

```text
Iteration 5 live smoke: fix write payload regression, large collection sync, and MCP read concurrency
```

Body:

```markdown
Iteration 5 live smoke on `feat/live-api-coverage-iteration-5` found that the live read path is improving, but the branch is not ready for routine live GitCode use.

Report: `project/handoffs/2026-06-24-live-api-iteration-5-smoke.md`

Passed:
- `go test ./...`
- `git diff --check`
- keychain token is visible to the installed binary
- `sync --live --issues` fetched 10 live issues from `urandon/gitcode-mcp-testing-polygon`
- `list`, `search`, `sync-status`, and positional `get` work against the live issue cache

Blocking gaps:
- MCP tools are currently read/cache-only and cannot bootstrap or refresh a live cache. Repo binding, live sync, index, doctor/readiness, auth status, and repo status are missing from MCP, so agents must fall back to CLI before MCP reads are useful.
- Large collection sync is not operationally safe. Wiki stress data caused long no-progress sync behavior, and `--timeout 5s` did not bound the command. Treat this as a generic collection-size problem, not wiki-specific.
- Empty wiki initialization is not supported by the currently wired v5 wiki-as-repository path. `urandon/gitcode-mcp` fails sync/create first-page attempts with 400 until a page is created manually in the UI. After manual initialization, sync works but normalizes `Home.md` as `wiki/Home.md.md`, and `create-page` still fails write-confirmation decoding.
- Live issue writes now return GitCode 400 for create/update. Likely cause: empty labels are serialized as `labels: []` even when the user did not request label mutation.
- `add-comment --live` reaches the provider but fails response decoding as malformed JSON; diagnostics incorrectly report `http_attempted:false`.
- `add-comment --live` can also fail credential resolution even while `auth status` sees the Keychain token; write commands need the same credential lookup path as auth/status/read probes.
- Parallel read-style commands on one cache path can hit writer lock contention and surface as `internal_error`, which is risky for MCP tool-call concurrency.

UX follow-ups:
- Root help advertises `mcp serve`, but `gitcode-mcp mcp --help` is unknown.
- Root help shows `--id`, while `get` actually requires a positional id.
- CLI provenance filters are not wired even though results expose `provenance`.
- Unsupported `add-label` should not classify as `internal_error`.
```

## Acceptance Recommendation

Do not merge iteration 5 as "live ready" until the MCP lifecycle gaps, empty-wiki initialization behavior, issue write regression, and large collection behavior are fixed. The current branch is useful as read-path progress and as evidence for the next implementation pass.
