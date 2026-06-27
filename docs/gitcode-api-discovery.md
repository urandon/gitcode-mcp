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
| Pull request review comments | Live comment payloads expose stable comment ids, discussion ids, body, and `updated_at`; parent PR list payload exposes `notes` and `updated_at`. | Bulk sync stages from list-comment payloads. A safe skip of the parent `ListPRComments` call needs a persisted parent comment-collection checkpoint; current cache stores individual comment revisions after listing. |
| Issue comments | Comment payloads expose stable ids, body, and `updated_at`; issue list payload exposes `comments` count and `updated_at`. | Not an independent bulk selector. Issue sync uses issue list revision metadata to avoid listing comments when the issue marker is unchanged. |
| Labels | No reliable update marker documented for the current cache surface. | Treat as full refresh or unsupported for metadata skip until discovery proves a marker. |
| Milestones | Adapter model includes `UpdatedAt`, but list behavior and persistence are not verified for collection sync. | Not yet a first-class bulk collection surface; do not report `skipped_by_revision`. |

## Evidence Rules

- Never commit credentials or private tokens.
- Prefer sanitized request/response fixtures.
- Record API version, date, host, and permission scope.
- Separate official docs from reverse-engineered behavior.
- Mark uncertain behavior explicitly.
