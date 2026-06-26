# Materialized Validation Scenarios

Task: `018-internal-gitcode-task-7-add-pr-adapter-with-list-detail-comments-routes-i`

## 018-internal-gitcode-task-7-add-pr-adapter-with-list-detail-comments-routes-i-scenario-1

PR list test caches pull_request records with fields id,number,html_url,state,title,body,user,labels,base,head; source_id derived from number.

Executable validation: run the mocked HTTP client product-path test `TestScenario018PRListDetailCommentsRoutes` in `internal/gitcode`. The test calls `HTTPClient.ListPRs`, captures the GET response from a local `httptest` server serving a pull request array, and asserts each decoded `PullRequest` record has `Kind == "pull_request"`, `SourceID == "PR-7"` (derived from `number: 7`), and all required fields (`ID`, `Number`, `HTMLURL`, `State`, `Title`, `Body`, `User`, `Labels`, `Base`, `Head`) populated. The record passes through `UnmarshalJSON` which sets `Kind` and `SourceID`.

## 018-internal-gitcode-task-7-add-pr-adapter-with-list-detail-comments-routes-i-scenario-2

PR detail returns cached record.

Executable validation: run the mocked HTTP client product-path test `TestScenario018PRListDetailCommentsRoutes` in `internal/gitcode`. After the PR list call, the test calls `HTTPClient.GetPR` with `PRRequest{Number: 7}`, receives the single PR detail from a local `httptest` server via `GET /api/v5/repos/{owner}/{repo}/pulls/7`, and asserts `Kind == "pull_request"`, `SourceID == "PR-7"`, `Number == 7`, `ID == "101"`.

## 018-internal-gitcode-task-7-add-pr-adapter-with-list-detail-comments-routes-i-scenario-3

PR comments cache pr_comment linked to parent.

Executable validation: run the mocked HTTP client product-path test `TestScenario018PRListDetailCommentsRoutes` in `internal/gitcode`. The test calls `HTTPClient.ListPRComments` with `PRRequest{Number: 7}`, receives a comment array from a local `httptest` server via `GET /api/v5/repos/{owner}/{repo}/pulls/7/comments`, and asserts each decoded `PRComment` record has `Kind == "pr_comment"`, `ID == "301"`, `DiscussionID == "DISC-7"`, `PRNumber == 7`, `Body == "looks good"`, `Author == "bob"`.

## 018-internal-gitcode-task-7-add-pr-adapter-with-list-detail-comments-routes-i-scenario-4

PR comment write receives 201 with {id,note_id,body} and caches with http_attempted: true.

Executable validation: run the mocked HTTP client product-path test `TestScenario018PRCommentWrite` in `internal/gitcode`. The test calls `HTTPClient.CreatePRComment` with `CreatePRCommentRequest{Body: "posted", Number: 7}`, the local `httptest` server responds `201` with `{"id":201,"note_id":301,"body":"posted"}`, and the test asserts the write result `Confirmed == true`, `ProviderStatus == "201"`, `RemoteID == "301"`, `Record.Kind == "pr_comment"`, `Record.ID == "301"`, `Record.Body == "posted"`. The write path passes through `writeConfirmedSchemaJSON` which sets `Confirmed: true` and `ConfirmedAt` on success, confirming http_attempted.

## 018-internal-gitcode-task-7-add-pr-adapter-with-list-detail-comments-routes-i-scenario-5

Route exclusion test: deployment-inhibited routes (pull_requests, merge_requests, review_comments) have zero calls on provider mock.

Executable validation: run the mocked HTTP client product-path test `TestScenario018PRListDetailCommentsRoutes` in `internal/gitcode`. The test records every `Method + URL.Path` seen by the local `httptest` server, then after all PR operations complete, iterates over all recorded paths and fails if any path contains the substrings `pull_requests`, `merge_requests`, or `review_comments`. The only paths recorded are `/api/v5/repos/{owner}/{repo}/pulls`, `/pulls/7`, and `/pulls/7/comments`, none of which match the deployment-inhibited patterns.
