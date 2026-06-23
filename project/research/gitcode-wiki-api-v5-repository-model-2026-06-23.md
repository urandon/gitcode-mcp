# GitCode Wiki API v5 Repository Model

Date: 2026-06-23

## Summary

GitCode Wiki can be accessed through the normal `/api/v5` repository file-content API by addressing the wiki as a sibling repository named `{repo}.wiki`.

This is stronger than the browser-observed `web-api.gitcode.com/api/v2/projects/wiki/*` route because it works with the existing token model, does not require browser cookies, and uses the same API family already used by repository file operations.

## Primary Finding

Use the repository file-content API against `{owner}/{repo}.wiki`:

```text
GET    /api/v5/repos/{owner}/{repo}.wiki/contents
GET    /api/v5/repos/{owner}/{repo}.wiki/contents/{path}
GET    /api/v5/repos/{owner}/{repo}.wiki/raw/{path}
POST   /api/v5/repos/{owner}/{repo}.wiki/contents/{path}
PUT    /api/v5/repos/{owner}/{repo}.wiki/contents/{path}
DELETE /api/v5/repos/{owner}/{repo}.wiki/contents/{path}
```

Observed behavior:

- `contents` without a path returns a directory listing.
- `contents/{path}` returns file metadata, including `sha`, and base64 file content.
- `raw/{path}` returns raw markdown content.
- `POST contents/{path}` creates a wiki file.
- `PUT contents/{path}` updates a wiki file when supplied with the current `sha`.
- `DELETE contents/{path}` deletes a wiki file when supplied with the current `sha`.
- After delete, raw read returns a not-found response.

## Authentication

The configured GitCode token worked for the `/api/v5` wiki-as-repository path with the existing project credential model.

Observed compatible token placements during smoke:

- `Authorization: Bearer {token}`
- `private-token: {token}`
- `access_token={token}` query parameter

Implementation should prefer the existing project credential model and avoid documenting query-token usage unless needed for compatibility tests, because query strings are easier to leak through logs.

## Content Encoding

Create and update require base64-encoded `content`.

A smoke test with plain markdown in `content` created an empty blob. The empty test file was deleted during cleanup.

Recommended write contract:

- CLI accepts plain markdown/body input.
- Adapter base64-encodes content before calling `/contents/{path}`.
- Update/delete first resolve current `sha` or require caller-supplied `sha` for explicit conflict handling.
- Write results should record remote commit/file metadata without leaking author email or raw token-bearing request details.

## Listing And Sync Shape

Directory listing entries include file and directory records. Wiki sync can traverse recursively:

1. Start at `GET /api/v5/repos/{owner}/{repo}.wiki/contents`.
2. Recurse into entries with `type: "dir"`.
3. Treat entries with `type: "file"` and supported wiki-renderable extensions as candidate wiki pages.
4. Read page bodies through `raw/{path}` for content, or through `contents/{path}` when `sha` and base64 body are needed together.

Product docs say Wiki supports renderable document formats beyond Markdown. Iteration 5 should decide whether the cache imports only Markdown-like formats first or records unsupported renderable files with explicit diagnostics.

## Evidence Sources

GitCode-owned references:

- `https://docs.gitcode.com/en/docs/help/home/org_project/wiki/wiki-intro`
- `https://gitcode.com/GitCode/GitCode-Docs`
- `https://gitcode.com/GitCode/gitcode-skills`

Relevant points from those references:

- GitCode Wiki is a repository-integrated product feature.
- Wiki has `Home`, `_Sidebar.md`, and `_Footer.md` conventions.
- Wiki content can be cloned as a Wiki repository.
- `GitCode-Docs` ordinary repository links to Wiki pages, but those linked wiki markdown pages are not present in the ordinary repository clone.
- `gitcode-skills` documents `/api/v5`, `GITCODE_TOKEN`, `/api/v5/user` token validation, and repository file-content endpoints, but not a dedicated Wiki CRUD/list API.

Live smoke evidence:

- Public GitCode docs wiki content was readable through `/api/v5/repos/{owner}/{repo}.wiki/contents/{path}` and `/raw/{path}`.
- Private test wiki content was readable through `/contents`, `/contents/{path}`, and `/raw/{path}`.
- Full create/read/update/read/delete smoke succeeded on a throwaway private wiki page using `/api/v5/repos/{owner}/{repo}.wiki/contents/{path}`.

All live evidence above is summarized with placeholders. Raw private repository coordinates, raw responses, author emails, trace ids, cookies, and tokens are intentionally omitted.

## Browser Web API Findings

Browser network traffic exposed these routes:

```text
GET    https://web-api.gitcode.com/api/v2/projects/wiki/detail
GET    https://web-api.gitcode.com/api/v2/projects/{owner}%2F{repo}.wiki/repository/file_list
POST   https://web-api.gitcode.com/api/v2/projects/wiki/create
PUT    https://web-api.gitcode.com/api/v2/projects/wiki/update
DELETE https://web-api.gitcode.com/api/v2/projects/wiki/delete
GET    https://web-api.gitcode.com/uc/api/v1/user/oauth/token
```

Token-only smoke showed that the configured `/api/v5` token is not accepted as a standalone credential for those browser `web-api` wiki routes. The observed OAuth/token bridge appears browser-session-backed.

Conclusion: browser `web-api` wiki routes should remain discovery/fallback evidence, not the primary implementation path for MCP.

## Product Impact

Iteration 5 should change direction:

- Prefer `/api/v5/repos/{owner}/{repo}.wiki/contents|raw` for wiki read/list/sync.
- Implement mocked tests for directory traversal, path encoding, base64 decode, raw reads, not-found behavior, create/update/delete, `sha` conflict handling, and base64 write encoding.
- Keep browser `web-api` routes out of the MVP unless a later product decision explicitly adopts browser-session credentials.
- Keep wiki HTTPS git clone as optional context, not a requirement for the MCP token model.

## Open Questions

- Should write commands expose wiki create/update/delete in iteration 5, or should iteration 5 ship read/list/sync first?
- Which file extensions should become cache records by default?
- Should `_Sidebar.md` and `_Footer.md` become ordinary wiki records, special metadata, or both?
- How should idempotency keys map to content writes where the remote API itself is `sha`/commit based?
- Should update/delete require caller-supplied `sha`, or should the CLI resolve latest `sha` by default and expose an explicit conflict mode?
