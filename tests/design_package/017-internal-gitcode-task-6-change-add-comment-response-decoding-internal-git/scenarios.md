# Materialized Validation Scenarios

Task: `017-internal-gitcode-task-6-change-add-comment-response-decoding-internal-git`

## 017-internal-gitcode-task-6-change-add-comment-response-decoding-internal-git-scenario-1

Add-comment POST to /api/v5/repos/{owner}/{repo}/issues/{num}/comments returns HTTP 201 with live-shaped body {id, note_id, body, created_at, user}; service decodes successfully with http_attempted: true; cached comment record present.

Executable validation: run mocked Go product-path tests that exercise the production HTTP client and service write graph. `TestConfirmedWriteOperations/SCN-GITCODE-ADD-COMMENT-LIVE-SHAPE-01` calls `HTTPClient.CreateIssueComment` against a local `httptest` GitCode API, receives HTTP 201 with live-shaped `{id,note_id,body,created_at,user}`, and asserts decoded comment identity, author, body, timestamp, parent issue number, and confirmed remote ID. `TestScenario017AddCommentLiveShapeCachesComment` calls `Service.AddComment` in live mode with a confirmed adapter result and asserts the comment is persisted in the cache record.

## 017-internal-gitcode-task-6-change-add-comment-response-decoding-internal-git-scenario-2

Malformed body returns http_attempted: true + schema_decode diagnostic.

Executable validation: run mocked Go product-path tests that exercise the production HTTP client decode boundary and downstream diagnostic classification. `TestScenario017AddCommentMalformedBodySchemaDecode` posts to a local `httptest` GitCode API that returns HTTP 201 with malformed JSON and asserts `CreateIssueComment` fails with `schema_decode` and no confirmed write. `TestScenario017AddCommentMalformedBodyDiagnosticHTTPAttempted` classifies the service write failure and asserts diagnostic code `schema_decode` with `HTTPAttempted=true`.
