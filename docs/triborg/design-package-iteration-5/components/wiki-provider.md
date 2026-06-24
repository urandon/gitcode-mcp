# Design Package Component: wiki-provider

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Wiki Provider

## Summary
The wiki-provider must replace the current slug-style GitCode wiki surface with the approved `/api/v5/repos/{owner}/{repo}.wiki` repository-content strategy. The component-local work is to add deterministic traversal, raw/base64 page materialization, sha-based writes, and browser-route exclusion for wiki sync/read/write paths.

## Top-Level Alignment
This component implements the approved Wiki-as-repository invariant for Task 6 and supplies component-local error evidence for Task 7 and offline validation evidence for Task 10. It owns `/contents`, `/contents/{path}`, and `/raw/{path}` behavior and keeps browser `web-api` wiki routes unwired from product runtime.

## Tasks

### Task 1: Add .wiki repository provider
Outcome IDs: outcome-6, outcome-7, outcome-10
Outcome Role: primary_product
Decommission IDs: decommission-3
Change Type: add
Description: Add a GitCode wiki-as-repository provider that treats each wiki as `{repo}.wiki` and reads pages through contents and raw file APIs. The provider is responsible for recursive directory traversal, path-safe endpoint construction, markdown/raw page materialization, base64 create/update payloads, and sha-based update/delete operations. It must route GitCode API and decode failures through the existing live error taxonomy instead of hiding them behind empty wiki results.
Existing Behavior / Reuse: Reuse the existing `internal/gitcode` client/provider interfaces, `WikiPage`, `CreateWikiPageRequest`, `UpdateWikiPageRequest`, write confirmation helpers, bounded HTTP reads, pagination helpers where applicable, and validation/error concepts. Replace the current `internal/gitcode` wiki endpoint builders and HTTP methods that use `/api/v5/repos/{owner}/{repo}/wiki` and `/wiki/{slug}` with `.wiki/contents|raw` behavior. Keep `internal/service` `BulkSyncWiki` and CLI `sync --wiki`, `create-page`, and `update-page` as callers, but change their provider contract from slug-only wiki pages to path/sha-backed wiki pages. Confirmed absent component-local functionality includes `.wiki/contents` endpoint construction, `.wiki/raw` reads, recursive contents traversal, base64 contents decoding, wiki delete, sha auto-resolution, and browser-route exclusion tests.
Detailed Design: Add component-local models `WikiContentsEntry`, `WikiContentsFile`, `WikiContentWriteRequest`, and `DeleteWikiPageRequest`; extend wiki write requests with `Path` and `Sha` while preserving `Slug` as a compatibility alias for path until callers are migrated. Add `DeleteWikiPage` to the GitCode client/provider wiki surface and make CLI/service callers pass path, body, message, and optional sha into the provider. Add endpoint builders `wikiContentsRootEndpoint(owner, repo)`, `wikiContentsPathEndpoint(owner, repo, path)`, and `wikiRawPathEndpoint(owner, repo, path)` that build `/api/v5/repos/{owner}/{repo}.wiki/contents`, `/contents/{path}`, and `/raw/{path}` with `{repo}.wiki` as the repository name and percent-encoding applied per path segment so nested wiki paths remain deterministic.

`ListWikiPages` fetches root contents, recursively expands directory entries, and returns normalized `WikiPage` records. Traversal is deterministic depth-first: normalize paths, sort entries lexicographically by normalized path at every directory, visit directories before files, keep visited directory and file-path sets, never follow symlink/submodule/unknown entry types, and stop with `schema_decode` if nesting exceeds 64 levels, a duplicate file path is returned, or a required directory entry field is malformed. Directory entries require non-empty `path` and `type`; file entries additionally require non-empty `sha`; malformed required fields classify as `schema_decode`. Import only regular files whose lower-case extension is `.md`, `.markdown`, `.mdown`, or `.mkd`; unsupported regular files are skipped with a non-fatal traversal diagnostic/count and are not cached as `wiki_page` records.

For body reads, prefer `GET /raw/{path}` for imported wiki pages because it returns the markdown body directly. `GET /contents/{path}` is still required for metadata and sha; if a file metadata response includes base64 `content`, decode it only as a fallback when raw body is unavailable by design in the called method, and classify missing content, invalid base64, unexpected encoding, missing path, or missing sha as `schema_decode`. `GetWikiPage` resolves metadata through `/contents/{path}`, reads the markdown body through `/raw/{path}`, derives `Title` from the basename without the imported markdown extension, sets `Slug` and remote id to the normalized path, and sets `version` from `sha`.

`CreateWikiPage` sends `POST /contents/{path}` with `WikiContentWriteRequest{content,message}` where `content` is base64-encoded body and `sha` is omitted. `UpdateWikiPage` and `DeleteWikiPage` send `PUT` or `DELETE /contents/{path}` with base64 content for update and a required sha for both operations; if the caller supplies explicit `--sha`, use it unchanged, otherwise auto-resolve current sha with `GET /contents/{path}` before writing. A stale sha or GitCode `409`, `400`, `401`, `403`, or `404` response is surfaced as `api_validation`; malformed write confirmations, missing path, or missing returned sha are surfaced as `schema_decode`. Enforce `decommission-3` by leaving browser `web-api.gitcode.com/api/v2/projects/wiki/*` out of endpoint builders, route registries, client methods, and default product tests; any observed product request to that host or route family fails the stubbed-provider test.
Acceptance Criteria: Wiki actor runs `gitcode-mcp sync --live --wiki --repo X` against a stubbed GitCode server exposing `/api/v5/repos/{owner}/{repo}.wiki/contents`, nested `/contents/{path}`, and `/raw/{path}`; traversal imports `.md`, `.markdown`, `.mdown`, and `.mkd` files into cache-visible wiki records with path-derived title, decoded body, version sha, deterministic order, and skipped unsupported files such as `.png` or `.txt` reported as non-fatal diagnostics, proven by `go test ./...`. A stubbed traversal case includes nested directories, unsorted entries, duplicate paths, malformed entries, and an unsupported file; valid pages sync, duplicate or malformed required fields produce `schema_decode`, unsupported files are not cached, and no browser `web-api` request is observed. CLI actor runs create, update, and delete wiki flows through live provider routes; create omits sha, update/delete use explicit `--sha` when provided or auto-resolve sha through `GET /contents/{path}`, requests contain base64 content where applicable, stale sha `409` is `api_validation`, invalid base64 or missing content metadata is `schema_decode`, and evidence is an executable stubbed-external-provider Go test plus `git diff --check`.
Workload: 2.0 MM

## Cross-Cutting Constraints
- Cache-first reads remain intact — wiki-provider produces normalized wiki records for the existing sync/cache path rather than requiring live reads from MCP
- Public-safe fixtures only — tests use sanitized `httptest` GitCode-shaped responses and must not store tokens, cookies, private coordinates, or raw browser captures
- Browser wiki routes stay internal only — product runtime must not call `web-api.gitcode.com/api/v2/projects/wiki/*` while implementing the default wiki provider
- Error classes stay distinct — API status failures and schema/decode failures from wiki routes must preserve the approved taxonomy for downstream CLI/MCP reporting

## Data And Control Flow
- CLI live wiki sync invokes the live adapter wiki surface — wiki-provider lists `.wiki/contents`, recursively expands directories, filters importable markdown extensions, fetches `/raw/{path}`, and returns normalized `WikiPage` records to sync/cache ownership
- CLI wiki create/update/delete invokes mutation methods — wiki-provider encodes body as base64, resolves or accepts sha for update/delete, sends `/contents/{path}` writes, verifies returned path/sha metadata, and reports write confirmation to the caller
- GitCode failure response enters the shared HTTP path — wiki-provider receives classified API validation, schema decode, credential, or transport errors and does not convert them to empty wiki result sets
- Offline tests drive product methods through `httptest` routes — external GitCode behavior is mocked while target wiki-provider code paths execute normally

## Component Interactions
- `wiki_provider` -> `live_adapter` — exposes list/get/create/update/delete wiki operations over normalized `WikiPage`, path, sha, and write-result contracts
- `wiki_provider` -> `cli_mutation_commands` — accepts live wiki create/update/delete requests with path, body, message, and sha inputs and returns write confirmation or typed diagnostics
- `wiki_provider` -> `cache_provenance_layer` — supplies normalized wiki page path, body, title, version sha, and remote id so sync can write `kind=wiki_page` records with provenance outside this component
- `wiki_provider` -> `error_classifier` — preserves status/decode/transport boundaries from wiki HTTP responses for CLI/MCP diagnostics
- `wiki_provider` -> `test_suite` — provides offline-testable behavior through `httptest` route assertions for traversal, path encoding, extension filtering, base64 payloads, sha conflicts, and browser-route absence

## Rationale
The approved architecture materially changes wiki-provider ownership from a slug-style wiki route to a repository-contents provider. This is the only configured component delta and it directly implements the primary product outcome for wiki sync/read/write behavior.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0201-run_attempt-1/final_message.txt`
