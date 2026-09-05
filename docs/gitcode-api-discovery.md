# GitCode API Discovery

## Purpose

Record what the tracker/wiki API can actually do before broad migration.

## Questions

- Which official or internal API docs are available?
- How are tracker issues created, updated, searched, labeled, and commented?
- How are wiki pages created, updated, searched, moved, and linked?
- How are attachments represented?
- What auth modes are supported?
- What pagination, rate limit, and timeout behavior exists?
- Are issue ids stable across project moves or imports?
- Can backlinks be created or discovered through API?
- Can we export enough state for rollback and audit?

## Collection Revision Markers

Discovery status for metadata-first sync:

| Collection | Candidate list metadata | Status |
| --- | --- | --- |
| Wiki pages | Contents/list entries expose `path`, `type`, and `sha`; the adapter maps `sha` to wiki page `revision`. | Confirmed usable for body-fetch skip. The sync engine compares the list revision to cached `remote_revision` and fetches the page body only for new, changed, incomplete, or marker-less records. |
| Issues | Live list responses expose stable `id`, numeric `number`, source `body`, labels, `comments` count, and `updated_at`. A live issue with one comment showed issue `updated_at` equal to the comment `updated_at`. | Usable for parent-first issue sync. The revision token includes list content, `updated_at`, and `comments`; collection traversal persists parents and updates durable comment work without issuing child reads. |
| Pull requests / merge requests | Live list payloads expose stable `id`, numeric `number`, state/status, labels, base/head refs, `diff_refs`, `notes`, and `updated_at`. | Bulk sync currently stages from list records and stores a list-version token as `remote_revision`. Future diff, commit, or review detail fetches should compare this marker before detail calls. |
| Pull request review comments | Live v5 comment payloads expose stable comment and discussion ids and nest replies under the root note's `reply` field. The read-only v4 discussion route has returned both nested discussions and a flat `data` envelope of timeline notes; matching diff notes expose richer `position` metadata but omit a reliable parent id for replies. | The adapter flattens v5 replies, derives their parent from the root note, accepts both observed v4 shapes, and enriches matching comment ids with v4 position metadata. Bulk sync then persists one ordered cached discussion. A safe skip of the parent `ListPRComments` call still needs a persisted parent comment-collection checkpoint. |
| Issue comments | Comment payloads expose stable ids, body, and `updated_at`; the repository aggregate includes `target.issue.id` and `target.issue.number`; issue list payload exposes `comments` count and `updated_at`. | An independent durable queue is populated by parent issue traversal. With a complete parent frontier, `--issue-comments` uses the aggregate collection and reconciles it to stable cached parents. Bounds or interruption replay from page 1; rate limiting stops without per-issue fan-out. The per-issue route remains the compatibility fallback. |
| Labels | No reliable update marker documented for the current cache surface. | Treat as full refresh or unsupported for metadata skip until discovery proves a marker. |
| Milestones | Adapter model includes `UpdatedAt`, but list behavior and persistence are not verified for collection sync. | Not yet a first-class bulk collection surface; do not report `skipped_by_revision`. |
| Push remote mirrors | The v5 list exposes stable `id`, `project_id`, destination `url`, force/private flags, update status, failure count/message, and update timestamps. The v5 item POST accepts `{"force":true}` for a manual trigger. | Implemented as explicit list, audited trigger, and bounded wait surfaces with sanitized cache projection. It is not a bulk frontier collection. |

## Issue Update Patch Compatibility

A live compatibility observation on 2026-08-30 showed that the v5 issue update
route can clear an assigned milestone when an otherwise valid JSON PATCH omits
`milestone`. The public API documentation describes the update fields but does
not document an `ETag`, `If-Match`, revision token, or another conditional-write
precondition for this route.

The adapter therefore treats omitted fields as public patch semantics while
handling the provider quirk at its boundary. Before a partial issue update it
reads the canonical issue, carries the existing milestone into the provider
payload, and leaves omitted labels absent. It then requires canonical readback
to confirm every requested field and to verify that the omitted milestone and
labels remain unchanged. A 2xx response alone is not write confirmation.

This read-modify-write compatibility path is not a claim of serializable
external writes. Without a provider precondition, another client can change an
issue between the preimage read and PATCH. Durable in-flight recovery and
typed conflict handling belong to the service reliability contract; callers
must not infer provider-level compare-and-swap from the preservation check.
The service closes the local replay/concurrency gap by atomically claiming the
idempotency key after that preimage GET and before PATCH, storing only hashed
field invariants. It can detect a changed preimage before a later safe retry and
can recover an ambiguous completed write through GET-only canonical readback.
It still cannot eliminate the provider's residual GET-to-PATCH race; only a
future GitCode conditional-write primitive could provide that guarantee.

## Pull Request Merge Write Boundary

GitCode pull request merge uses the v5 route:

```http
PUT /api/v5/repos/{owner}/{repo}/pulls/{number}/merge
Authorization: Bearer $GITCODE_TOKEN
Content-Type: application/json

{"merge_method":"merge"}
```

The provider does not expose a demonstrated conditional or server-side
idempotency primitive for this mutation. The adapter therefore reads the
canonical PR first, checks the optional expected head SHA, invokes the
service's atomic claim callback, and makes exactly one PUT attempt. It then
requires canonical PR readback with the same number, a non-empty provider id,
and `state=merged`.

Transport failures, 5xx, 429, and readback failures are ambiguous even when a
response was received; the generic retry transport must not replay the PUT.
The merge request clears Go's replayable-body hook, so the standard client also
cannot transparently replay it or follow a body-preserving 307/308 redirect.
The service stores a hash of the claimed head SHA and keeps the audit row
`in_progress`. Same-key recovery is GET-only and can finalize success only when
the canonical merged PR still matches that head hash. This is a local duplicate
mutation fence, not a claim of provider-level compare-and-swap. Retry claims
compare-and-swap the exact failed audit generation observed before the
preimage request, which prevents a delayed concurrent caller from reclaiming a
failure it did not observe. Preflight failures before that claim do not mutate
the audit row and therefore cannot overwrite another caller's active fence.
Once readback confirms the merge, the audit records
a cache-refresh-pending state before any cache write and records `succeeded`
only after cache confirmation; restart recovery repeats GET and cache repair,
never PUT. An already-merged preimage invokes the same local claim callback
before returning its no-PUT confirmation, so a cache-settlement failure still
retains the head fingerprint required by recovery. Post-claim audit transitions
also compare the owned generation and expected status; late PUT outcomes cannot
overwrite a recovery settlement that has already advanced the row.

## Repository Push Remote Mirrors

The repository list route is:

```http
GET /api/v5/repos/{owner}/{repo}/push_remote_mirrors
Authorization: Bearer $GITCODE_TOKEN
Accept: application/json
```

A minimal live probe on 2026-07-30 confirmed that the configured personal token
is accepted and that the response is a JSON array. Observed item fields are
`id`, `project_id`, `url`, `force`, `is_private`, `update_status`,
`number_of_failures`, `message`, `created_at`, `last_update_at`, and
`last_successful_update_at`.

The GitCode settings UI also uses a browser-coupled v2 route:

```http
GET https://web-api.gitcode.com/api/v2/projects/{owner}%2F{repo}/push_remote_mirrors?project_id={owner}%252F{repo}
```

The normal configured API token received HTTP 403 from that surface. The
adapter therefore uses only the v5 repository route and does not reproduce
browser cookies, JWTs, page metadata, device identifiers, or double-encoded UI
query behavior.

Frontend compatibility discovery on 2026-07-30 showed that the UI's **Sync
Now** action posts `force: true` to the selected mirror item. The corresponding
v5 repository route is:

```http
POST /api/v5/repos/{owner}/{repo}/push_remote_mirrors/{mirror-id}
Authorization: Bearer $GITCODE_TOKEN
Content-Type: application/json

{"force":true}
```

A bounded live probe with a configured personal token reached the mirror
operation and returned the provider's typed synchronization-cooldown response,
confirming that the v5 POST is PAT-compatible. The adapter makes one POST
attempt, then confirms a successful trigger through a sanitized v5 list
readback. Because no server-side idempotency guarantee has been demonstrated,
an ambiguous POST or readback result remains locally `in_progress` and blocks a
same-key replay.

Observed update states include `default`, `failed`, `update`, `processing`,
`started`, and `finished`. The wait surface treats only `failed` and `finished`
as terminal and returns `timeout` when its bounded polling interval expires.

Push mirror destination URLs are credential-bearing inputs. The adapter removes
URL user-info, query strings, and fragments during decode; invalid or
unsupported URL forms become `[redacted]`. Only that sanitized representation
may reach the service, cache, CLI, MCP, logs, tests, or snapshots.

## Issue State Update Transitions

Sanitized live discovery on `2026-07-13` confirmed that issue read states and write transitions use different values. A state-only `PATCH` accepted `{"state":"close"}` and `{"state":"reopen"}` without a title, returned canonical `closed` and `open` states, and preserved the existing title and body. Sending the public/read value `{"state":"closed"}` was rejected with a validation response requiring one of `reopen` or `close`.

The CLI, MCP, service, and adapter request contract therefore remains `open|closed`. The HTTP adapter translates those values to `reopen|close` only at the wire boundary and requires a separate issue readback with the requested canonical state before confirming the write.

## Issue List Milestone Identity And Response Diagnostics

Sanitized live inspection on `2026-08-17` confirmed that the issue collection endpoint can return a nested milestone with its positive identity in `number` and no `id` field. The milestone collection used the same `number` identity shape. The adapter accepts `id` for backward compatibility and falls back to `number` only when `id` is absent; all existing positive-integer and title/date validation still applies.

The same incident exposed an error-classification bug: the provider returned a complete `application/json` array, but the nested milestone schema error was reported as malformed JSON. Response handling now keeps four cases separate without logging response bodies:

- incomplete JSON or a short transport body is `partial_response`;
- syntactically invalid complete JSON is `malformed_response`;
- a non-JSON success response is `unexpected_content_type`;
- valid JSON that does not match a captured adapter shape is `schema_decode`.

Only the first three response-level failures are retried for the same bounded page, using the configured read retry budget. Schema drift is deterministic and is not retried. Exhausted diagnostics expose only the endpoint, normalized media type, response byte count, decode offset, and attempt count; raw bodies, headers, cookies, and tokens remain excluded.

After those retries are exhausted, the issues collection adapter preserves any complete `IssueSummary` array elements that precede a truncated or syntactically malformed item. It returns that validated prefix together with the original typed response error. The service commits the usable prefix, reports both successes and one collection failure, stops pagination at the damaged page, and records only a non-complete frontier. This is recovery, not silent success: a later run must refetch the page, and a failure before the first complete element still returns zero usable records.

## List Ordering Parameters

Live discovery on `2026-06-28` used the public `openharmony/arkcompiler_runtime_core` repository because it has large issue and pull request collections.

Issues:

```http
GET /api/v5/repos/{owner}/{repo}/issues?state=all&order_by=updated_at&sort=desc&page=1&per_page=3
```

This returned HTTP 200 and records ordered by recent `updated_at`. Created-time ordering also accepted `order_by=created_at&sort=desc`.

Pull requests:

```http
GET /api/v5/repos/{owner}/{repo}/pulls?state=all&order_by=updated_at&direction=desc&page=1&per_page=3
```

This returned HTTP 200 and records ordered by recent `updated_at`. The issue-style `sort=desc` parameter returned HTTP 400 with a message that `direction` is required, so keep issue and pull request query builders collection-specific. The UI-coupled `order_by_sort=updated_at_desc` also returned HTTP 200 for pull requests, but the adapter uses `order_by=updated_at&direction=desc` because the API error points to `direction`.

## Repository-Wide Issue Comments

Sanitized live discovery on 2026-07-13 confirmed the repository aggregate:

```http
GET /api/v5/repos/{owner}/{repo}/issues/comments?page=1&per_page=100
Authorization: Bearer $GITCODE_TOKEN
```

The route returns a JSON array. Pagination uses `total_count` and `total_page` response headers. Each comment exposes its stable comment id, body, timestamps, and a nested parent object at `target.issue` containing both provider `id` and numeric `number`. The adapter requests one page at a time so the service can commit page-level progress. Parent reconciliation requires the provider id and number to agree when both resolve; missing or conflicting parents are reported as `issue_comment_reconciliation` instead of being silently attached.

Aggregate traversal is safe only after the issue parent frontier is complete. A bounded or interrupted run restarts at page 1 and repeats idempotent upserts, because newer comments can shift page offsets. Queue items become complete and stale cached comments are removed only after a full collection pass. If the route returns an explicit unsupported response, the service uses the existing per-issue endpoint. Transient and rate-limit errors do not trigger that request-amplifying fallback.

## Pull Request Issue Relations

Live discovery on a public-safe testing repository confirmed an explicit relation endpoint:

```http
POST /api/v5/repos/{owner}/{repo}/pulls/{pr_number}/issues
Content-Type: application/json

[issue_number]
```

The response is a JSON array of linked issue records. Confirmation should require that the returned array contains the requested issue number. A repeated POST with the same array returned the same linked issue list, so the adapter treats successful readback as idempotent. JSON object payloads and string/object `issue_nums` shapes were rejected during discovery; keep the adapter payload as a raw JSON number array.

## Wiki Write Routes And Browser URLs

Wiki writes use the v5 contents API for the `{repo}.wiki` repository:

```http
POST /api/v5/repos/{owner}/{repo}.wiki/contents/{path}
PUT /api/v5/repos/{owner}/{repo}.wiki/contents/{path}
DELETE /api/v5/repos/{owner}/{repo}.wiki/contents/{path}
```

GitCode wiki UI links use the singular browser route:

```http
/{owner}/{repo}/wiki/{page_slug}
```

The browser slug must keep nested wiki paths inside one route segment, so path separators inside the page slug are URL-encoded. For example, `Evidence/Dogfood/Report.md` becomes `/wiki/Evidence%2FDogfood%2FReport.md`. Wiki create, update, and delete commands normalize missing extensions to `.md` before calling the provider. Write output reports the API path, cache path, remote slug, and normalized browser URL so an operator can compare the live page route with the cached record without relying on ambiguous `/wikis/...` SPA routes.

Delete confirmation is inverted from create/update confirmation. After a successful provider DELETE, the confirmation GET for the exact normalized wiki path should return a typed not-found response. That not-found proves the deleted page is absent. A successful GET for the same path means deletion is not confirmed, while auth, route, conflict, rate-limit, and transient provider errors remain write failures.

## Pull Request Review Discussions

Earlier live discovery for inline review comment metadata used the GitLab-compatible v4 discussion surface:

```http
GET /api/v4/projects/{owner}%2F{repo}/merge_requests/{iid}/discussions
Authorization: Bearer $GITCODE_TOKEN
```

Inline notes can be returned as `DiffNote` entries inside discussion `notes`. A 2026-07-13 public sample also returned an object envelope with flat notes/events in `data`; those entries carry `discussion_id` directly and may contain the same diff fields. The adapter accepts both shapes. When present, `position` and `original_position` include fields such as `position_type`, `base_sha`, `start_sha`, `head_sha`, `old_path`, `new_path`, `old_line`, `new_line`, `line_code`, `start_line_code`, `patchset_iid`, `diff_id`, `version_sha`, and `is_outdated`. Bearer and `PRIVATE-TOKEN` authentication were both accepted in the bounded probe; the adapter keeps Bearer auth consistent with other reads. Browser dogfood on 2026-06-29 showed that v4-created inline comments can disappear from private or unauthenticated discussion views even though authenticated views and v4 readback expose them. Treat this v4 surface as legacy metadata evidence, not as the frontend-compatible write contract.

No repository-wide PR comment or discussion endpoint was found. Read-only probes of `/api/v5/repos/{owner}/{repo}/pulls/comments` and `/pulls/discussions` returned parameter-validation errors because the route interpreted the final segment as a pull request identifier. Collection must therefore remain a rate-limit-aware per-PR walk over an explicitly selected cached parent set; it must stop on rate limit and must not enable an unbounded body crawl by default.

The bounded sizing sample used five public repositories and the 100 most recently updated PR metadata rows from each. The list-level `notes` marker was almost always nonzero but includes system timeline events, so it is not a user-comment count. Fifteen PRs were then selected across low, middle, and upper `notes <= 30` strata and read with at most one 100-record v5 page and one v4 page each. The sanitized result contained 192 v5 roots, 7 nested replies, 199 total v5 notes, 245,323 UTF-8 body bytes, and a 4 KiB size floor of 221 chunks; 20 bodies exceeded 4 KiB. The v4 representation contained 511 timeline notes across 504 discussion ids, including 22 inline notes. The two surfaces are not additive: unmatched v4 events must not become duplicate RAG text.

For conditional planning, the observed repository strata required 1.00-1.27 size-based chunks per fetched v5 note, 1.11 overall. This is a calibration bound, not a corpus confidence interval: the sample intentionally selected comment-bearing PRs, recent-list `notes` does not measure user-comment prevalence, and unseen large bodies leave the global upper bound open. Estimate future RAG work from measured v5 note bodies in the selected queue, not from a fixed multiplier such as two chunks per PR.

Creating an inline review discussion uses the v5 pull request comments surface:

```http
POST /api/v5/repos/{owner}/{repo}/pulls/{number}/comments
Authorization: Bearer $GITCODE_TOKEN
Content-Type: application/json

{
  "body": "Review text",
  "path": "path/to/file.go",
  "line": 42,
  "new_line": 42,
  "position": 42
}
```

The v5 create endpoint expects `Authorization: Bearer ...`. GitCode accepts body-only and `path` plus `line` payloads, but those create timeline comments rather than private-mode-visible inline review comments. A payload that repeats the 1-based current-side file line as `line`, `new_line`, and `position` creates an inline review comment that appears in both private and authenticated browser views. Here `position` is not a diff-hunk ordinal. The adapter therefore accepts `line` as the ordinary coordinate and derives all three provider fields from it. Because the v5 POST response can be sparse, success additionally requires list/discussion readback of the returned `note_id` with matching current path and line. Missing anchor evidence is ambiguous; a different anchor is a typed mismatch. Request-derived metadata alone is not write confirmation.

Replying inside an existing review discussion uses the official v5 endpoint added in the 2025-07-31 API release:

```http
POST /api/v5/repos/{owner}/{repo}/pulls/{number}/discussions/{discussion_id}/comments
Authorization: Bearer $GITCODE_TOKEN
Content-Type: application/json

{"body":"Reply text"}
```

Live discovery on 2026-07-13 returned HTTP 201 with a sparse `{id, note_id, body}` response where `id` was the discussion id and `note_id` was the new comment id. The v5 comment list then exposed the created note inside the root comment's `reply` array. The v4 read representation returned both notes with the same inline position but left `in_reply_to_id` and `parent_id` empty, so the adapter must derive parentage from the v5 nesting rather than from v4 note fields. The v4 write route returned HTTP 403 with guidance to use v5 and must not be used for replies.

The reply adapter requires both `discussion_id` and the expected parent/root comment id. It validates that pair before mutation, reuses an already matching reply on retry, sends the audited v5 write, and requires a list readback matching comment id, discussion id, parent id, and body before reporting success.

## Evidence Rules

- Never commit credentials or private tokens.
- Prefer sanitized request/response fixtures.
- Record API version, date, host, and permission scope.
- Separate official docs from reverse-engineered behavior.
- Mark uncertain behavior explicitly.
