# Design Package Component: internal-gitcode

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: GitCode Live Adapter

## Summary
GitCode Live Adapter owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Change Bounded wiki tree traversal in ListWikiPage
Outcome IDs: outcome-3
Outcome Role: primary_product
Decommission IDs: decommission-3
Change Type: change
Description: Implement the `Bounded wiki tree traversal in ListWikiPages (internal/gitcode/wiki_adapter.go)` delta inside `GitCode Live Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `GitCode Live Adapter` boundaries where available and add only the missing `Bounded wiki tree traversal in ListWikiPages (internal/gitcode/wiki_adapter.go)` behavior.
Detailed Design: Add or change `Bounded wiki tree traversal in ListWikiPages (internal/gitcode/wiki_adapter.go)` so it satisfies `Replace outer loop wrapper approach for bounded wiki sync. ListWikiPages uses internal stack-based traversal with context cancellation check at each directory level and progress emission per page/tree-level. No outer loop wrapper. Traversal stops within current directory level on cancellation and returns committed records.`. Keep ownership inside `GitCode Live Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Wiki sync with recursive tree provider, context cancelled mid-traversal: traversal stops within current directory level. PartialSyncError returned with records committed so far. Stack-based walker checks ctx.Done() at each directory entry. No outer loop wrap pattern exists in code..
Workload: 1 MM

### Task 2: Add Empty wiki detection (internal/gitcode/wiki_ad
Outcome IDs: outcome-4
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Implement the `Empty wiki detection (internal/gitcode/wiki_adapter.go)` delta inside `GitCode Live Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `GitCode Live Adapter` boundaries where available and add only the missing `Empty wiki detection (internal/gitcode/wiki_adapter.go)` behavior.
Detailed Design: Add or change `Empty wiki detection (internal/gitcode/wiki_adapter.go)` so it satisfies `Detect empty/uninitialized wiki via 400/404 response on GET /api/v5/repos/{owner}/{repo}.wiki/contents. Return typed empty_wiki diagnostic with actionable remediation text. Differentiate empty wiki from other 400 causes by response body pattern or secondary probe. Optionally bootstrap empty wiki via POST Home.md with base64-encoded body returning 201 + follow-up GET confirmation; if bootstrap not attempted, return unsupported_wiki_uninitialized diagnostic.`. Keep ownership inside `GitCode Live Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: GET wiki/contents returns 400 or 404 -> empty_wiki diagnostic class with remediation text. Create-page against empty wiki -> either POST Home.md (201 + confirm GET) or unsupported_wiki_uninitialized diagnostic. Empty wiki response not classified as api_validation..
Workload: 1 MM

### Task 3: Change Wiki path normalization (internal/gitcode/w
Outcome IDs: outcome-5
Outcome Role: primary_product
Decommission IDs: decommission-4
Change Type: change
Description: Implement the `Wiki path normalization (internal/gitcode/wiki_adapter.go)` delta inside `GitCode Live Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `GitCode Live Adapter` boundaries where available and add only the missing `Wiki path normalization (internal/gitcode/wiki_adapter.go)` behavior.
Detailed Design: Add or change `Wiki path normalization (internal/gitcode/wiki_adapter.go)` so it satisfies `Strip double extension during wiki path normalization. Store remote Home.md as wiki/Home.md not wiki/Home.md.md. Apply normalization at record creation time so the cache path field reflects the correct basename. Handle subdirectory paths without adding duplicate .md extensions.`. Keep ownership inside `GitCode Live Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Remote page named Home.md synced; cached record path field is wiki/Home.md. Existing test fixtures updated. go test ./internal/gitcode/... passes..
Workload: 1 MM

### Task 4: Add Wiki create-page write confirmation (internal/
Outcome IDs: outcome-5
Outcome Role: primary_product
Decommission IDs: decommission-4
Change Type: add
Description: Implement the `Wiki create-page write confirmation (internal/gitcode/wiki_adapter.go)` delta inside `GitCode Live Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `GitCode Live Adapter` boundaries where available and add only the missing `Wiki create-page write confirmation (internal/gitcode/wiki_adapter.go)` behavior.
Detailed Design: Add or change `Wiki create-page write confirmation (internal/gitcode/wiki_adapter.go)` so it satisfies `When POST /api/v5/repos/{owner}/{repo}.wiki/contents/{path} returns 201 but response body lacks path and sha fields, perform follow-up GET contents/{path} to confirm write. On confirmation (200 with matching content and sha), cache record with http_attempted: true. On confirmation failure or mismatch, return write_confirmation_incomplete diagnostic without marking write as cached-successful.`. Keep ownership inside `GitCode Live Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Create-page POST returns 201 with missing path/sha -> follow-up GET confirms with matching content+sha -> record cached with http_attempted: true. Follow-up GET returns mismatched content or fails -> write_confirmation_incomplete diagnostic, record not cached as definitely written..
Workload: 1 MM

### Task 5: Change Issue label omission serialization (interna
Outcome IDs: outcome-7
Outcome Role: primary_product
Decommission IDs: decommission-5
Change Type: change
Description: Implement the `Issue label omission serialization (internal/gitcode/issue_adapter.go)` delta inside `GitCode Live Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `GitCode Live Adapter` boundaries where available and add only the missing `Issue label omission serialization (internal/gitcode/issue_adapter.go)` behavior.
Detailed Design: Add or change `Issue label omission serialization (internal/gitcode/issue_adapter.go)` so it satisfies `Fix issue create/update JSON serialization so labels key is omitted from request body when no label mutation was requested. Use omitempty plus nil guard: when labels are nil or unset, omit the field entirely. Do not send labels: [] (empty array) when user did not request label mutation.`. Keep ownership inside `GitCode Live Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Issue create without labels: HTTP body lacks labels key entirely. Issue update changing only title: HTTP body lacks labels key entirely. Serialized JSON inspected; labels property absent from serialized body..
Workload: 1 MM

### Task 6: Change Add-comment response decoding (internal/git
Outcome IDs: outcome-7
Outcome Role: primary_product
Decommission IDs: decommission-6
Change Type: change
Description: Implement the `Add-comment response decoding (internal/gitcode/comment_adapter.go)` delta inside `GitCode Live Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `GitCode Live Adapter` boundaries where available and add only the missing `Add-comment response decoding (internal/gitcode/comment_adapter.go)` behavior.
Detailed Design: Add or change `Add-comment response decoding (internal/gitcode/comment_adapter.go)` so it satisfies `Fix add-comment response decoding to accept live comment response shape: JSON object with id, note_id, body, created_at, user fields. Decode into struct mapping live fields. Set http_attempted: true when provider was contacted. On decode failure, return schema_decode diagnostic with http_attempted: true (not http_attempted: false). Cache decoded comment record on success.`. Keep ownership inside `GitCode Live Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: Add-comment POST to /api/v5/repos/{owner}/{repo}/issues/{num}/comments returns HTTP 201 with live-shaped body {id, note_id, body, created_at, user}; service decodes successfully with http_attempted: true; cached comment record present. Malformed body returns http_attempted: true + schema_decode diagnostic..
Workload: 1 MM

### Task 7: Add PR adapter with list/detail/comments routes (i
Outcome IDs: outcome-8
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Implement the `PR adapter with list/detail/comments routes (internal/gitcode/pr_adapter.go)` delta inside `GitCode Live Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `GitCode Live Adapter` boundaries where available and add only the missing `PR adapter with list/detail/comments routes (internal/gitcode/pr_adapter.go)` behavior.
Detailed Design: Add or change `PR adapter with list/detail/comments routes (internal/gitcode/pr_adapter.go)` so it satisfies `Implement PR adapter: ListPRs via GET /api/v5/repos/{owner}/{repo}/pulls (paginated, cancellable, cache records kind: pull_request with number-derived source_id). GetPR via GET /pulls/{number} (returns individual cached record). ListPRComments via GET /pulls/{number}/comments (caches records kind: pr_comment linked to parent by discussion_id or PR number). AddPRComment via POST /pulls/{number}/comments (decodes 201 with {id, note_id, body}, caches with http_attempted: true). Exclude deployment-inhibited routes (pull_requests, merge_requests, review_comments) from route table.`. Keep ownership inside `GitCode Live Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: PR list test caches pull_request records with fields id,number,html_url,state,title,body,user,labels,base,head; source_id derived from number. PR detail returns cached record. PR comments cache pr_comment linked to parent. PR comment write receives 201 with {id,note_id,body} and caches with http_attempted: true. Route exclusion test: deployment-inhibited routes (pull_requests, merge_requests, review_comments) have zero calls on provider mock..
Workload: 1 MM

### Task 8: Validate Adapter integration tests (internal/gitco
Outcome IDs: outcome-3, outcome-4, outcome-5, outcome-7, outcome-8
Outcome Role: supporting_evidence
Decommission IDs: decommission-3, decommission-4, decommission-5, decommission-6
Change Type: validate
Description: Implement the `Adapter integration tests (internal/gitcode/adapter_test.go)` delta inside `GitCode Live Adapter`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `GitCode Live Adapter` boundaries where available and add only the missing `Adapter integration tests (internal/gitcode/adapter_test.go)` behavior.
Detailed Design: Add or change `Adapter integration tests (internal/gitcode/adapter_test.go)` so it satisfies `Write mocked tests: wiki empty detection (400/404 -> empty_wiki diagnostic). Wiki path normalization (Home.md -> wiki/Home.md). Wiki create-page write confirmation (follow-up GET). Issue label omission (labels key absent). Add-comment response decoding (live shape and malformed body). PR list/detail/comments routes. PR deployment-inhibited route exclusion. Wiki bounded tree traversal with cancellation.`. Keep ownership inside `GitCode Live Adapter`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: go test ./internal/gitcode/... passes. All adapter behaviors verified with mocked HTTP providers. Each test inspects exact HTTP request shape, response decoding, diagnostic classification, and cache record fields..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `GitCode Live Adapter` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `GitCode Live Adapter` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
