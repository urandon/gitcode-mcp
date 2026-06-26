# Scenarios

## 012-internal-gitcode-task-2-add-empty-wiki-detection-internal-gitcode-wiki_ad-scenario-1
**GET wiki/contents returns 400 or 404 -> empty_wiki diagnostic class with remediation text.**

- Mocked httptest server returns HTTP 400 with body `{"message":"wiki not found"}` to `GET /api/v5/repos/example-owner/example-repo.wiki/contents`.
- `HTTPClient.ListWikiPages` returns `ErrEmptyWiki` with `DiagnosticCode() == "empty_wiki"` and error message containing remediation text ("empty or uninitialized").
- Not classified as `ErrAPIValidation`.

- Mocked httptest server returns HTTP 404 with body `{"message":"wiki is empty"}` to the contents endpoint.
- `HTTPClient.ListWikiPages` returns `ErrEmptyWiki` instead of `ErrNotFound`.

## 012-internal-gitcode-task-2-add-empty-wiki-detection-internal-gitcode-wiki_ad-scenario-2
**Create-page against empty wiki -> empty_wiki diagnostic with remediation text.**

- Mocked httptest server returns HTTP 400 with body `{"message":"wiki not found"}` on POST to `/api/v5/repos/example-owner/example-repo.wiki/contents/Home.md`.
- `HTTPClient.CreateWikiPage` returns `ErrEmptyWiki` with `DiagnosticCode() == "empty_wiki"` and remediation text ("empty or uninitialized").
- The write path (`bytesWithOptions`) checks `isWikiEmptyResponse` on 400/404 responses before falling through to `statusError`, ensuring write paths also return typed `empty_wiki` diagnostics instead of `api_validation`.

## 012-internal-gitcode-task-2-add-empty-wiki-detection-internal-gitcode-wiki_ad-scenario-3
**Empty wiki response not classified as api_validation.**

- Mocked httptest server returns HTTP 400 with body `{"message":"invalid repo name"}` (not matching empty-wiki patterns).
- `HTTPClient.ListWikiPages` returns `ErrAPIValidation` with `DiagnosticCode() == "api_validation"`.
- Proves differentiation: 400 without empty-wiki patterns falls through to `ErrAPIValidation`.
