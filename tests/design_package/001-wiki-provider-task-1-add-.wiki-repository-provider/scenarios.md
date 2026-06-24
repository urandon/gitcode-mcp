# Scenarios: Add .wiki repository provider

## 001-wiki-provider-task-1-add-.wiki-repository-provider-scenario-1

Wiki actor runs `gitcode-mcp sync --live --wiki --repo X` against a stubbed GitCode server exposing `/api/v5/repos/{owner}/{repo}.wiki/contents`, nested `/contents/{path}`, and `/raw/{path}`; traversal imports `.md`, `.markdown`, `.mdown`, and `.mkd` files into cache-visible wiki records with path-derived title, decoded body, version sha, deterministic order, and skipped unsupported files such as `.png` or `.txt` reported as non-fatal diagnostics, proven by `go test ./...`.

Executable validation:

- Run focused Go tests for `TestScenario001WikiContentsRootTraversal` and `TestCLIStartupPlanSelectsLiveProvider/SCN-MOCKAPI-LIVE-SYNC-VALID`.
- The `internal/gitcode` test drives the production GitCode HTTP client through a local `httptest.Server` exposing `.wiki/contents`, nested contents, and `.wiki/raw` routes.
- The `cmd/gitcode-mcp` test drives the real CLI startup seam with `sync --live --wiki` and verifies cache-visible live wiki records are written without fixture wiki leakage.

Expected result:

- Imported markdown wiki pages are returned in deterministic path order with path-derived titles, raw bodies, and sha revisions.
- Unsupported `.png`/`.txt` files are skipped and not cached.
- The validation remains offline and uses only stubbed external GitCode-compatible HTTP dependencies.

## 001-wiki-provider-task-1-add-.wiki-repository-provider-scenario-2

A stubbed traversal case includes nested directories, unsorted entries, duplicate paths, malformed entries, and an unsupported file; valid pages sync, duplicate or malformed required fields produce `schema_decode`, unsupported files are not cached, and no browser `web-api` request is observed.

Executable validation:

- Run focused Go tests for `TestScenario002WikiMalformedEntrySchemaDecode`, `TestScenario003WikiDuplicatePathDedup`, `TestScenario004WikiNestingLimit`, and `TestScenario010BrowserRouteExclusion`.
- These tests exercise the production `ListWikiPages` and `GetWikiPage` paths against local stubbed GitCode responses.
- Error assertions require malformed or duplicate content metadata to surface through the schema/decode error path, represented in this repository by `ErrPartialResponse`.

Expected result:

- Malformed entries, duplicate file paths, and over-deep traversal fail non-zero through schema/decode errors instead of empty wiki results or transport failures.
- Browser `web-api.gitcode.com/api/v2/projects/wiki/*` routes are not called by default wiki runtime paths.
- Unsupported files remain absent from returned pages.

## 001-wiki-provider-task-1-add-.wiki-repository-provider-scenario-3

CLI actor runs create, update, and delete wiki flows through live provider routes; create omits sha, update/delete use explicit `--sha` when provided or auto-resolve sha through `GET /contents/{path}`, requests contain base64 content where applicable, stale sha `409` is `api_validation`, invalid base64 or missing content metadata is `schema_decode`, and evidence is an executable stubbed-external-provider Go test plus `git diff --check`.

Executable validation:

- Run focused Go tests for `TestScenario005WikiRawReadBody`, `TestScenario006WikiCreateBase64NoSha`, `TestScenario007WikiUpdateShaAutoresolve`, `TestScenario008WikiUpdateExplicitSha`, and `TestScenario009WikiDeleteStaleSha409`.
- These tests drive production `GetWikiPage`, `CreateWikiPage`, `UpdateWikiPage`, and `DeleteWikiPage` live-provider routes against local `httptest.Server` endpoints.
- Run `go test ./... -count=1` and `git diff --check` after focused scenarios.

Expected result:

- Reads use `/contents/{path}` metadata plus `/raw/{path}` body and derive title/revision from path/sha.
- Create sends base64 body to `POST /contents/{path}` without sha.
- Update uses caller-supplied sha without auto-resolution or resolves sha via `GET /contents/{path}` when absent.
- Delete resolves sha when absent and maps stale `409` to the API-validation conflict path.
- Full offline Go tests and whitespace checks pass.
