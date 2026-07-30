# Push Mirror Inspection

`gitcode-mcp` can inspect the push mirrors configured for a repository without
copying browser cookies or session tokens into agent configuration.

The feature is intentionally read-only. Creating, updating, deleting, and
manually triggering mirrors require separate captured API contracts and are not
part of this surface.

## CLI

```sh
gitcode-mcp push-mirrors \
  --repo example-owner/example-repo \
  --format json
```

The command performs an explicit live read, returns a deterministic list, and
refreshes sanitized `push_remote_mirror` records in the selected cache.

## MCP

The equivalent MCP tool is `list_push_remote_mirrors`:

```json
{
  "repo_id": "example-owner/example-repo"
}
```

The tool is read-only and remains available when
`GITCODE_MCP_TOOL_ACCESS=read`. It still requires a configured live GitCode
credential because the operation refreshes state from GitCode.

## API Compatibility

The adapter uses the documented GitCode v5 route:

```http
GET /api/v5/repos/{owner}/{repo}/push_remote_mirrors
Authorization: Bearer <configured-token>
Accept: application/json
```

The older browser-observed `/api/v2/projects/.../push_remote_mirrors` route is
not used. It requires browser-session credentials and would couple the product
to cookies, device metadata, and double-encoded UI request parameters.

## Credential Redaction

Mirror destinations may contain credentials for the target hosting provider.
Redaction therefore happens while the adapter decodes the response, before the
service result or cache graph is constructed:

- URL user-info is removed;
- the entire query string is removed;
- fragments are removed;
- destinations that cannot be parsed as an absolute `http`, `https`, `ssh`, or
  `git` URL become `[redacted]`;
- supported URLs embedded in provider messages receive the same treatment.

Raw responses and unredacted mirror destinations must not appear in logs,
fixtures, CLI output, MCP results, cache records, snapshots, or issue/PR
evidence.

## Cache Projection

Each successful live list item refreshes one deterministic cache record:

- stable id: `PUSHMIRROR-<remote-id>`;
- record type and remote type: `push_remote_mirror`;
- path: `push-mirrors/<remote-id>.md`;
- body: sanitized destination plus non-secret status, failure count, and update
  timestamps.

After the explicit refresh, existing cache-first list, get, search, and export
surfaces can inspect the sanitized projection without network access.

Authentication failures, forbidden access, HTML/login responses, malformed
JSON, and partial responses are returned as typed diagnostics. The adapter does
not fall back to browser cookies or ask for browser JWTs.
