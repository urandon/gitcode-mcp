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
| Issues | Live list responses expose stable `id`, numeric `number`, source `body`, labels, `comments` count, and `updated_at`. A live issue with one comment showed issue `updated_at` equal to the comment `updated_at`. | Usable for current issue sync. The revision token includes list content, `updated_at`, and `comments`; unchanged tokens skip the per-issue comments list call. Future full-detail issue sync should compare this marker before adding detail calls. |
| Pull requests / merge requests | Live list payloads expose stable `id`, numeric `number`, state/status, labels, base/head refs, `diff_refs`, `notes`, and `updated_at`. | Bulk sync currently stages from list records and stores a list-version token as `remote_revision`. Future diff, commit, or review detail fetches should compare this marker before detail calls. |
| Pull request review comments | Live comment payloads expose stable comment ids, discussion ids, body, and `updated_at`; parent PR list payload exposes `notes` and `updated_at`. The v4 merge request discussions API exposes richer diff-note `position` metadata for inline comments. | Bulk sync stages from list-comment payloads. A safe skip of the parent `ListPRComments` call needs a persisted parent comment-collection checkpoint; current cache stores individual comment revisions after listing. Schema version 13 persists discussion rows and per-comment current/original position rows when that metadata is available. |
| Issue comments | Comment payloads expose stable ids, body, and `updated_at`; issue list payload exposes `comments` count and `updated_at`. | Not an independent bulk selector. Issue sync uses issue list revision metadata to avoid listing comments when the issue marker is unchanged. |
| Labels | No reliable update marker documented for the current cache surface. | Treat as full refresh or unsupported for metadata skip until discovery proves a marker. |
| Milestones | Adapter model includes `UpdatedAt`, but list behavior and persistence are not verified for collection sync. | Not yet a first-class bulk collection surface; do not report `skipped_by_revision`. |

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

## Pull Request Issue Relations

Live discovery on a public-safe testing repository confirmed an explicit relation endpoint:

```http
POST /api/v5/repos/{owner}/{repo}/pulls/{pr_number}/issues
Content-Type: application/json

[issue_number]
```

The response is a JSON array of linked issue records. Confirmation should require that the returned array contains the requested issue number. A repeated POST with the same array returned the same linked issue list, so the adapter treats successful readback as idempotent. JSON object payloads and string/object `issue_nums` shapes were rejected during discovery; keep the adapter payload as a raw JSON number array.

## Pull Request Review Discussions

Live discovery for inline review comments uses the GitLab-compatible v4 discussion surface:

```http
GET /api/v4/projects/{owner}%2F{repo}/merge_requests/{iid}/discussions
PRIVATE-TOKEN: $GITCODE_TOKEN
```

Inline notes are returned as `DiffNote` entries inside discussion `notes`. When present, `position` and `original_position` include fields such as `position_type`, `base_sha`, `start_sha`, `head_sha`, `old_path`, `new_path`, `old_line`, `new_line`, `line_code`, `start_line_code`, `patchset_iid`, `diff_id`, `version_sha`, and `is_outdated`.

Creating an inline review discussion uses the same v4 surface:

```http
POST /api/v4/projects/{owner}%2F{repo}/merge_requests/{iid}/discussions
PRIVATE-TOKEN: $GITCODE_TOKEN
Content-Type: application/json

{
  "body": "Review text",
  "position": {
    "position_type": "text",
    "base_sha": "BASE_SHA",
    "start_sha": "START_SHA",
    "head_sha": "HEAD_SHA",
    "old_path": "path/to/file.go",
    "new_path": "path/to/file.go",
    "new_line": 42
  }
}
```

The v4 endpoint expects token auth in `PRIVATE-TOKEN`; do not send the v5 `Authorization: Bearer ...` header for this request. The adapter confirms a write by reading back a matching `DiffNote` path and line. If the POST response is sparse but the write is confirmed, the cache stores a request-derived current position using the PR base/head SHAs and later resync can replace or enrich it with server-provided `position` metadata.

## Evidence Rules

- Never commit credentials or private tokens.
- Prefer sanitized request/response fixtures.
- Record API version, date, host, and permission scope.
- Separate official docs from reverse-engineered behavior.
- Mark uncertain behavior explicitly.
