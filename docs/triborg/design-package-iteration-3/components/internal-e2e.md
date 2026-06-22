# Design Package Component: internal-e2e

This file is copied from the approved Triborg design package during implementator preflight.

Now I have complete knowledge of the actual project source. Let me produce the final component design.

# Component Design: E2E Test Harness

## Summary
The `internal/e2e/` package adds a build-tag-gated live two-cache validation harness creating two independent `*cache.SQLiteStore` instances from the same operator-provided live GitCode repository, performing one gated live issue write via `service.Service.CreateIssue` with `WriteModeLive`, re-syncing cache A, independently syncing cache B from the remote, and proving equivalent `cache.Record` content between both caches. All test output passes through `gitcode.RedactText` at emission point with a post-test scan for raw token and `Authorization:` header patterns.

## Top-Level Alignment
This component implements architecture component `a16` and request Task 5 only. It exercises the existing `*gitcode.HTTPClient` (via `gitcode.NewHTTPClient`), `*cache.SQLiteStore` (via `cache.NewSQLiteStore`), `*service.Service` (via `service.NewWithClient`), and `gitcode.RedactText` (from `internal/gitcode/redaction.go`). The harness is excluded from default `go test ./...` through the `//go:build e2e` build tag. All live sync internals, live write internals, credential acquisition, and cache persistence remain owned by their respective components (a2-a7, a4). Dependent component interfaces (`service.Service.SyncToCache`, `service.Service.CreateIssue`, `cache.SQLiteStore.ListRecords`, `cache.SQLiteStore.RecordCounts`, `gitcode.HTTPClient.ListIssues`, `gitcode.HTTPClient.GetIssue`, `gitcode.HTTPClient.ListWikiPages`, `gitcode.RedactText`) already exist in the project source tree and are confirmed present in `internal/service/service.go`, `internal/cache/records.go`, `internal/cache/sqlite.go`, and `internal/gitcode/redaction.go`.

## Tasks

### Task 1: Add two-cache live e2e test harness
Outcome IDs: outcome-5
Outcome Role: primary_product
Decommission IDs: none
Change Type: add
Description: Add `TestE2ELiveTwoCache` in a new `internal/e2e/` package gated by `//go:build e2e`. The test constructs a shared `*gitcode.HTTPClient` from operator env vars, creates two independent `*cache.SQLiteStore` instances in separate `t.TempDir()` directories, wires each through `service.NewWithClient`, discovers live issue IIDs and wiki slugs via `client.ListIssues`/`client.ListWikiPages` pagination loops, syncs cache A via per-alias `svcA.SyncToCache`, performs one live issue write via `svcA.CreateIssue` with `WriteModeLive` and a unique idempotency key, confirms the write via a `client.GetIssue` retry loop, snapshots a single alias list after confirmation for both caches, independently syncs cache B from the same snapshot list, computes normalized digests from `store.ListRecords` keyed by `type:remote_id` comparing `{Type, Title, normalizedBody, Status}`, asserts equivalence including the created issue, and verifies redaction by post-hoc scanning recorded raw messages for the actual token and `Authorization:` prefix patterns.
Existing Behavior / Reuse: Reuses `gitcode.NewHTTPClient` (from `internal/gitcode/http_client.go:32`), `gitcode.HTTPClient` with methods `ListIssues`, `GetIssue`, `ListWikiPages` (from `internal/gitcode/http_client.go:56,68,88`), `cache.NewSQLiteStore` (from `internal/cache/sqlite.go`), `cache.SQLiteStore.AddRepository`/`ListRecords`/`RecordCounts`/`Close` (from `internal/cache/accessors.go`, `internal/cache/records.go`, `internal/cache/sqlite.go`), `cache.RecordFilter`, `cache.Record`, `cache.RecordCounts`, `cache.RepositoryBinding`, `cache.RepositoryScopeIssues`, `cache.RepositoryScopeWiki` (from `internal/cache/models.go:69,91,191,204`), `service.NewWithClient` (from `internal/service/service.go:34`), `service.Service.SyncToCache`/`CreateIssue` (from `internal/service/service.go:753,841`), `service.SyncRequest`, `service.SyncResult`, `service.WriteCommandRequest`, `service.WriteCommandResult`, `service.WriteModeLive` (from `internal/service/types.go:283,307,549,552,556,572`), `gitcode.Config`, `gitcode.PaginationConfig`, `gitcode.IssueListRequest`, `gitcode.IssueRequest`, `gitcode.WikiListRequest` (from `internal/gitcode/client.go:26,36`, `internal/gitcode/models.go:10,14,18`), `gitcode.RedactText` (from `internal/gitcode/redaction.go:102`). No `internal/e2e` package currently exists.
Detailed Design:

**Test file and build tag.** Create file `internal/e2e/two_cache_test.go` in package `e2e`. The file begins with `//go:build e2e`. Imports: `testing`, `context`, `fmt`, `os`, `path/filepath`, `strconv`, `strings`, `sync`, `time`, `regexp`, `gitcode` (from `gitcode-mcp/internal/gitcode`), `cache` (from `gitcode-mcp/internal/cache`), `service` (from `gitcode-mcp/internal/service`), and the standard library `crypto/rand` for idempotency key generation (or `math/rand` with a seed from `time.Now().UnixNano()`).

**Redaction-safe testLogger.** Define an unexported struct `testLogger` with fields `t *testing.T`, `token string`, `owner string`, `repo string`, `messages []string`, `mu sync.Mutex`. The method `logf(format string, args ...interface{})` formats the message via `fmt.Sprintf`, applies `gitcode.RedactText(msg, token, owner, repo)` to produce the redacted string, appends the *raw unredacted* formatted message to `messages`, and calls `lg.t.Logf("%s", redacted)`. The method `errorf(format string, args ...interface{})` does the same but calls `lg.t.Errorf`; it also appends the raw message to `messages`. The method `fatalf(format string, args ...interface{})` does the same as `errorf` then calls `lg.t.FailNow()`. All test log/error/fatal paths pass through these methods.

**Environment and configuration.** `TestE2ELiveTwoCache` reads from `os.Getenv`: `GITCODE_TOKEN` (required), `GITCODE_E2E_OWNER` (required), `GITCODE_E2E_REPO` (required), and optionally `GITCODE_E2E_BASE_URL` (defaults to `"https://gitcode.com/api/v4"`). If any required variable is missing, the test calls `t.Skipf` with a skip message naming only the missing env var name (not its value): `"missing required env: GITCODE_TOKEN"`, etc. The test constructs `lg := &testLogger{t: t, token: token, owner: owner, repo: repo}`. A shared `*gitcode.HTTPClient` is created via `gitcode.NewHTTPClient(gitcode.Config{BaseURL: baseURL, Token: token, Timeout: 30 * time.Second, MaxRetries: 3, Pagination: gitcode.PaginationConfig{PerPage: 100}})`.

**Service factory helper.** Define an unexported function `newService(ctx context.Context, t *testing.T, client *gitcode.HTTPClient, owner, repo, baseURL string) (*service.Service, *cache.SQLiteStore, string)`:
1. Creates `dir := t.TempDir()`.
2. Constructs `cachePath := filepath.Join(dir, "gitcode-cache.db")`.
3. Creates `store, err := cache.NewSQLiteStore(ctx, cachePath)`; errors call `t.Fatalf`.
4. Registers `t.Cleanup(func() { _ = store.Close() })`.
5. Constructs `repoID := owner + "/" + repo`.
6. Adds the repository binding via `store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repoID, Owner: owner, Name: repo, APIBaseURL: baseURL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}})`.
7. Creates `svc := service.NewWithClient(store, client)`.
8. Returns `svc, store, repoID`.

**Alias discovery.** Define an unexported struct `syncAlias` with fields `RemoteType string` (`"issue"` or `"wiki"`), `RemoteID string` (IID as string for issues, slug string for wikis), `Number int` (only for issues), and `Slug string` (only for wikis). Define an unexported function `discoverAliases(ctx context.Context, lg *testLogger, client *gitcode.HTTPClient, owner, repo string) ([]syncAlias, error)` that:
1. Paginates issues: calls `client.ListIssues(ctx, gitcode.IssueListRequest{Owner: owner, Repo: repo, PerPage: 100})` in a loop. For each `Page[IssueSummary]` result, iterates `Items` and appends `syncAlias{RemoteType: "issue", RemoteID: strconv.Itoa(s.Number), Number: s.Number}`. Uses `result.NextPage` comparison (from `Page[T].NextPage`) to determine if the next page exists; if `NextPage == 0` or `NextPage <= currentPage`, breaks the loop; otherwise sets `req.Page = NextPage` and continues. Propagates errors.
2. Paginates wikis: same pattern with `client.ListWikiPages(ctx, gitcode.WikiListRequest{Owner: owner, Repo: repo, PerPage: 100})`, appending `syncAlias{RemoteType: "wiki", RemoteID: s.Slug, Slug: s.Slug}`.
3. Returns the combined slice.

**Sync cache A.** The test constructs `svcA, storeA, repoID := newService(ctx, t, client, owner, repo, baseURL)`. For each `alias := range discoverAliases(...)`, calls `svcA.SyncToCache(ctx, service.SyncRequest{RepoID: repoID, RemoteAlias: alias.RemoteID})`. On `SyncResult.Status == "succeeded"`, logs via `lg.logf`. On error, collects the alias and calls `lg.errorf` with the error string (redacted via `RedactText`). After all aliases are attempted, if any failures remain, calls `lg.fatalf("cache A sync failures: %d/%d", failCount, totalCount)`.

**Live write.** The test generates a unique idempotency key: `"e2e-idem-" + randomHex(16)` where `randomHex` reads 8 bytes from `crypto/rand` and encodes to hex. It generates a unique title: `"e2e-test-" + randomHex(8)`. Constructs `req := service.WriteCommandRequest{Mode: service.WriteModeLive, Owner: owner, Repo: repo, Title: title, Body: "created by redacted two-cache e2e test", IdempotencyKey: idemKey}`. Calls `result, err := svcA.CreateIssue(ctx, req)`. On success, extracts `remoteNumber := result.RemoteNumber`. If `err != nil`, calls `lg.fatalf` with the error string after passing through `gitcode.RedactText`.

**Write confirmation retry loop.** After `CreateIssue` succeeds, enters a wait-or-retry loop polling `client.GetIssue(ctx, gitcode.IssueRequest{Owner: owner, Repo: repo, Number: remoteNumber})` with a total timeout of 30 seconds and a 2-second interval between attempts. Uses `time.Ticker` and a `context.WithTimeout`. On each iteration, logs `lg.logf("polling issue #%d ... attempt %d", remoteNumber, attempt)`. On success, breaks the loop. If the context deadline is exceeded without a successful `GetIssue`, calls `lg.fatalf("created issue #%d not visible on remote after 30s", remoteNumber)`. This proves eventual consistency of the write within the timeout window.

**Snapshot aliases after write confirmation.** After the retry loop confirms write visibility, builds `snapshotAliases` by copying all original discovery aliases and explicitly appending `syncAlias{RemoteType: "issue", RemoteID: strconv.Itoa(remoteNumber), Number: remoteNumber}` (the created issue). This snapshot list is used for both the cache A re-sync and the cache B initial sync, ensuring both caches sync against the identical set of remote resources.

**Re-sync cache A after write.** Iterates `snapshotAliases` and calls `svcA.SyncToCache(ctx, service.SyncRequest{RepoID: repoID, RemoteAlias: alias.RemoteID})` for each alias. This ensures cache A reflects the post-write remote state including the newly created issue.

**Sync cache B.** Constructs `svcB, storeB, repoIDB := newService(ctx, t, client, owner, repo, baseURL)` (note: `repoIDB` will be the same string `owner+"/"+repo` because both caches bind the same logical repo — `newService` always constructs `owner+"/"+repo`). Iterates the same `snapshotAliases` list and calls `svcB.SyncToCache(ctx, service.SyncRequest{RepoID: repoIDB, RemoteAlias: alias.RemoteID})` for each alias. Failure collection and reporting mirror cache A's pattern.

**Pre-digest RemoteID format validation.** Before computing digests, the test calls `storeA.ListRecords(ctx, cache.RecordFilter{RepoID: repoID, Limit: 0})` and `storeB.ListRecords(ctx, cache.RecordFilter{RepoID: repoIDB, Limit: 0})` (where `Limit: 0` indicates no row limit on the query, as the `RecordFilter.Limit` zero value is passed through to the SQL query). For every `cache.Record` from both caches, asserts `Record.RemoteID != ""`. For records where `Record.Type == "issue"`, asserts `strconv.Atoi(Record.RemoteID)` returns nil (the RemoteID is a numeric IID string). For records where `Record.Type == "wiki"`, asserts `Record.RemoteID` is non-empty. Any violation calls `lg.fatalf("RemoteID format mismatch: type=%s remote_id=%q", record.Type, record.RemoteID)`.

**Digest computation with body normalization.** Define an unexported struct `normalizedRecord` with fields `Type, Title, Body, Status string`. Define an unexported function `computeDigest(ctx context.Context, lg *testLogger, store *cache.SQLiteStore, repoID string) map[string]normalizedRecord` that:
1. Calls `store.ListRecords(ctx, cache.RecordFilter{RepoID: repoID, Limit: 0})`.
2. For each `cache.Record`, normalizes `Body` by: trimming leading/trailing whitespace via `strings.TrimSpace`, replacing all `\r\n` with `\n`, and collapsing any trailing newlines to a single trailing `\n` (if present). The normalized body is `strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))`.
3. Constructs the key as `record.Type + ":" + record.RemoteID`.
4. Stores `normalizedRecord{Type: record.Type, Title: record.Title, Body: normalizedBody, Status: record.Status}`.
5. Returns the map.

Note that comment ordering parity is not asserted across the two caches because independent syncs may serialize comments in API-returned order, which can vary between sequential paginated responses.

**Equivalence assertion.** Computes `digestA := computeDigest(ctx, lg, storeA, repoID)` and `digestB := computeDigest(ctx, lg, storeB, repoIDB)`.
1. For every key `k` present in both `digestA` and `digestB`, asserts `digestA[k] == digestB[k]` using `reflect.DeepEqual`. If any mismatch is found, calls `lg.errorf("digest mismatch for %s: cacheA={type:%s status:%s} cacheB={type:%s status:%s}", k, a.Type, a.Status, b.Type, b.Status)` — using `"[REDACTED]"` for Title and Body in the error message to avoid exposing content in diffs.
2. Asserts `len(digestA) == len(digestB)` (same key set size). If sizes differ, reports keys present in one but not the other by type (e.g., "cache A has 5 wiki keys not in cache B") without exposing the specific URL-encoded key values (which may contain repo/owner in path context).
3. Asserts the created issue's key (`"issue:" + strconv.Itoa(remoteNumber)`) is present in both `digestA` and `digestB` with `Status` matching `"open"` or `"opened"`.

**Comment count parity.** Queries `countsA, err := storeA.RecordCounts(ctx, repoID)` and `countsB, err := storeB.RecordCounts(ctx, repoIDB)`. Asserts `countsA.Comments == countsB.Comments`. Since both stores use the same logical repo identity (`owner+"/"+repo`) and each `RecordCounts` query is scoped to its own store's binding, the resulting integers are compared directly. If the comparison fails, `lg.errorf` reports the integer values: `"comment count mismatch: cacheA=%d cacheB=%d", countsA.Comments, countsB.Comments`.

**Redaction verification.** The test registers `t.Cleanup` to run a post-test scan of all messages in `lg.messages`. For each raw message:
1. Checks `strings.Contains(message, lg.token)` — must return `false`. The token value is the exact string from `GITCODE_TOKEN` env var.
2. Checks for `Authorization:` header pattern via a precompiled `regexp.MustCompile(`(?i)authorization:\s*\S+`)` — must find no matches on any line of each message.
If any violation is found, calls `lg.t.Errorf("REDACTION FAILURE: %s found in test output at message index %d", patternDescription, idx)`. The error message reports the message index and the pattern type that matched, but does NOT include the violating message text or the matched substring.

**Cleanup.** `t.TempDir()` handles directory cleanup on test completion. Each store's `Close()` is registered via `t.Cleanup` in the `newService` helper. No manual temp-directory removal is needed.

Acceptance Criteria: An operator runs `go test -run TestE2ELiveTwoCache -tags=e2e ./internal/e2e/` with `GITCODE_TOKEN`, `GITCODE_E2E_OWNER`, and `GITCODE_E2E_REPO` set in the environment. Evidence type: API test. Product surface: the e2e test binary exercises the live `*gitcode.HTTPClient` against the GitCode REST API and two independent `*cache.SQLiteStore` cache databases wired through `*service.Service`. Expected outcome: both caches contain equivalent `cache.Record` content including the created issue, as verified by `(type, remote_id) -> {type, title, normalized_body, status}` digest comparison and comment count parity; test output contains no raw token value and no `Authorization:` header patterns, as verified by post-hoc scan of all recorded log/error messages against the raw token string (exact match) and the `Authorization:\s*\S+` regex.
Workload: 1.0 MM

## Cross-Cutting Constraints
- Default offline determinism — the harness uses the `//go:build e2e` build tag and is excluded from normal `go test ./...` unless explicitly selected with `-tags=e2e`.
- Public-safe diagnostics — every test log/error/fatal path passes through a `testLogger` that invokes `gitcode.RedactText` with `token`, `owner`, and `repo` as sensitive terms at emission point; a post-test scan of all recorded raw messages verifies the raw token string never appears and no `Authorization::\s+\S+` pattern is present.
- Remote source-of-truth proof — cache B is independently synced from the live remote after the live write and write confirmation retry loop, with both caches using an identical snapshot alias list captured after write confirmation to prevent time-skew diffs.
- Comment ordering not asserted — independent syncs may serialize comments in API-returned order, which can vary between sequential paginated responses; only comment count parity is asserted across caches.

## Data And Control Flow
- Operator env vars (`GITCODE_TOKEN`, `GITCODE_E2E_OWNER`, `GITCODE_E2E_REPO`, optional `GITCODE_E2E_BASE_URL`) → `t.Skipf` if missing or `gitcode.Config` → `gitcode.NewHTTPClient` → shared `*gitcode.HTTPClient` reused by both service instances.
- `t.TempDir()` → `cache.NewSQLiteStore(cachePath)` → `store.AddRepository(owner, repo, baseURL, scopes)` → `service.NewWithClient(store, httpClient)` → `svcA` and `svcB` on separate temp directories.
- `httpClient.ListIssues` + `httpClient.ListWikiPages` pagination loops using `Page[T].NextPage` → `[]syncAlias{RemoteType, RemoteID, Number, Slug}`.
- Per-alias `svcA.SyncToCache(ctx, SyncRequest{RepoID, RemoteAlias})` → internal `fetchAndStage` dispatches to `httpClient.GetIssue` or `httpClient.GetWikiPage` → `cache.SQLiteStore.UpsertSyncGraph` persists records.
- `svcA.CreateIssue(ctx, WriteCommandRequest{Mode: WriteModeLive, ...})` → `service.Service.executeWrite` → `httpClient.CreateIssue` → returns `WriteCommandResult{RemoteNumber}`.
- `httpClient.GetIssue(ctx, IssueRequest{Owner, Repo, Number})` retry loop with 30s timeout, 2s interval → proves remote accepted the write.
- Snapshot alias list: copy discovery aliases + append created issue alias → used for both cache A re-sync and cache B sync.
- Per-alias re-sync `svcA.SyncToCache` from snapshot list.
- Second `t.TempDir()` → independent `cache.NewSQLiteStore` → `storeB.AddRepository` → `service.NewWithClient` → `svcB` → per-alias `svcB.SyncToCache` from same snapshot alias list.
- Pre-digest validation: `storeA.ListRecords(ctx, RecordFilter{RepoID, Limit: 0})` + `storeB.ListRecords(ctx, RecordFilter{RepoIDB, Limit: 0})` → iterate all `cache.Record` values, assert `Record.RemoteID != ""` and format consistency (issues: numeric IID; wikis: non-empty slug).
- `computeDigest: store.ListRecords → map[string]normalizedRecord` with body normalization (`strings.TrimSpace`, `\r\n`→`\n`, trailing newline collapse) keyed by `Record.Type + ":" + Record.RemoteID` with `{Type, Title, normalizedBody, Status}` → `reflect.DeepEqual` assertion across matching keys.
- `storeA.RecordCounts(ctx, repoID).Comments` and `storeB.RecordCounts(ctx, repoIDB).Comments` → integer comparison.
- `testLogger.logf`/`errorf`/`fatalf` → `gitcode.RedactText(message, token, owner, repo)` → `t.Logf`/`t.Errorf`/`t.Fatalf` with redacted output + append raw message to `messages` → `t.Cleanup` post-test scan of raw `messages` for `strings.Contains(raw, token)` and `regexp.MatchString(AuthorizationHeaderPattern, line)`.

## Component Interactions
- `internal/e2e` → `internal/gitcode` — constructs `*gitcode.HTTPClient` from operator env vars via `NewHTTPClient(Config{BaseURL, Token, Timeout, MaxRetries, Pagination})`; uses `ListIssues(IssueListRequest{Owner, Repo, PerPage: 100})` returning `Page[IssueSummary]` with `NextPage` for paginated alias enumeration; uses `ListWikiPages(WikiListRequest{Owner, Repo, PerPage: 100})` similarly; uses `GetIssue(IssueRequest{Owner, Repo, Number})` for write confirmation retry loop; uses `RedactText(text, sensitiveTerms...)` at every log/error/fatal emission point via `testLogger`.
- `internal/e2e` → `internal/cache` — creates independent `*cache.SQLiteStore` instances via `NewSQLiteStore(ctx, cachePath)`, adds repository bindings via `AddRepository(ctx, RepositoryBinding{RepoID, Owner, Name, APIBaseURL, Scopes})`, reads records via `ListRecords(ctx, RecordFilter{RepoID, Limit: 0})`, reads counts via `RecordCounts(ctx, repoID)`, and closes via `Close()`.
- `internal/e2e` → `internal/service` — calls `service.NewWithClient(store, client)` to construct wired service instances for cache A and cache B; uses `svc.SyncToCache(ctx, SyncRequest{RepoID, RemoteAlias})` for per-alias sync dispatching; uses `svc.CreateIssue(ctx, WriteCommandRequest{Mode: WriteModeLive, Owner, Repo, Title, Body, IdempotencyKey})` for the gated live write.
- `internal/e2e` → operator environment — consumes `GITCODE_TOKEN`, `GITCODE_E2E_OWNER`, `GITCODE_E2E_REPO`, and optionally `GITCODE_E2E_BASE_URL` from env vars; never persists raw secret material in test fixtures, cache assertions, or test output.

## Rationale
The approved architecture assigns the two-cache live parity harness to `internal/e2e` as component `a16`. Since no existing `internal/e2e` package exists, adding one build-tag-gated test is the smallest component-local change that satisfies Task 5 and outcome-5 while reusing existing `internal/gitcode`, `internal/cache`, and `internal/service` contracts exactly as they exist in the project source tree. The digest uses `cache.Record` fields `Type` (kind), `RemoteID`, `Title`, `Body`, and `Status` — all confirmed at `internal/cache/models.go:91-109`. The test uses `service.NewWithClient` (confirmed at `service.go:34`), `svc.SyncToCache` with `SyncRequest{RemoteAlias}` (confirmed at `service.go:753`), and `svc.CreateIssue` with `WriteModeLive` (confirmed at `service.go:841` and `types.go:552`). `gitcode.RedactText` is confirmed at `internal/gitcode/redaction.go:102`. The snapshot alias list captured after write confirmation ensures both caches sync against the identical set of remote resources. The write confirmation retry loop with 30s timeout and 2s interval proves eventual consistency. Body normalization (`\r\n`→`\n`, whitespace trim) prevents formatting diffs from independent syncs. The `repoID` for both stores is identical (`owner+"/"+repo`), ensuring `RecordCounts` queries both scope to the same logical binding identity. Comment ordering parity is explicitly excluded from the assertion contract. The redaction verification scans raw messages for the actual token string (exact match) and the authorization header regex pattern, rather than generic `sensitiveTerms` substring matching.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-1364-run_attempt-1/final_message.txt`
