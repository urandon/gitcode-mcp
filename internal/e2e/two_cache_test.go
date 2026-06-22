//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

const defaultE2EBaseURL = "https://api.gitcode.com/api/v5"

type testLogger struct {
	t        *testing.T
	token    string
	owner    string
	repo     string
	mu       sync.Mutex
	messages []string
}

func (lg *testLogger) logf(format string, args ...any) {
	lg.emit(func(msg string) { lg.t.Logf("%s", msg) }, format, args...)
}

func (lg *testLogger) errorf(format string, args ...any) {
	lg.emit(func(msg string) { lg.t.Errorf("%s", msg) }, format, args...)
}

func (lg *testLogger) fatalf(format string, args ...any) {
	lg.emit(func(msg string) { lg.t.Errorf("%s", msg) }, format, args...)
	lg.t.FailNow()
}

func (lg *testLogger) emit(write func(string), format string, args ...any) {
	raw := fmt.Sprintf(format, args...)
	redacted := gitcode.RedactText(raw, lg.token, lg.owner, lg.repo)
	lg.mu.Lock()
	lg.messages = append(lg.messages, raw)
	lg.mu.Unlock()
	write(redacted)
}

func (lg *testLogger) verifyRedaction() {
	authHeaderPattern := regexp.MustCompile(`(?i)authorization:\s*\S+`)
	lg.mu.Lock()
	messages := append([]string(nil), lg.messages...)
	lg.mu.Unlock()
	for idx, message := range messages {
		if lg.token != "" && strings.Contains(message, lg.token) {
			lg.t.Errorf("REDACTION FAILURE: raw token found in test output at message index %d", idx)
		}
		if authHeaderPattern.MatchString(message) {
			lg.t.Errorf("REDACTION FAILURE: Authorization header pattern found in test output at message index %d", idx)
		}
	}
}

type syncAlias struct {
	RemoteType string
	RemoteID   string
	Number     int
	Slug       string
}

type normalizedRecord struct {
	Type   string
	Title  string
	Body   string
	Status string
}

func TestE2ELiveTwoCache(t *testing.T) {
	token := requiredEnv(t, "GITCODE_TOKEN")
	owner := requiredEnv(t, "GITCODE_E2E_OWNER")
	repo := requiredEnv(t, "GITCODE_E2E_REPO")
	baseURL := strings.TrimSpace(os.Getenv("GITCODE_E2E_BASE_URL"))
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("GITCODE_E2E_API_BASE_URL"))
	}
	if baseURL == "" {
		baseURL = defaultE2EBaseURL
	}

	lg := &testLogger{t: t, token: token, owner: owner, repo: repo}
	t.Cleanup(lg.verifyRedaction)

	ctx := context.Background()
	client, err := gitcode.NewHTTPClient(gitcode.Config{
		BaseURL:    baseURL,
		Token:      token,
		Timeout:    30 * time.Second,
		MaxRetries: 3,
		Pagination: gitcode.PaginationConfig{PerPage: 100},
	})
	if err != nil {
		lg.fatalf("create GitCode client: %v", err)
	}

	aliases, err := discoverAliases(ctx, lg, client, owner, repo)
	if err != nil {
		lg.fatalf("discover aliases: %v", err)
	}
	lg.logf("discovered aliases: issues=%d wiki=%d", countAliases(aliases, "issue"), countAliases(aliases, "wiki"))

	svcA, storeA, repoIDA := newService(ctx, t, client, owner, repo, baseURL)
	syncAliases(ctx, lg, svcA, repoIDA, aliases, "cache A initial sync")

	idemKey := "e2e-idem-" + randomHex(t, 8)
	title := "e2e-test-" + randomHex(t, 4)
	writeResult, err := svcA.CreateIssue(ctx, service.WriteCommandRequest{
		Mode:           service.WriteModeLive,
		Owner:          owner,
		Repo:           repoIDA,
		RepoID:         repoIDA,
		Title:          title,
		Body:           "created by redacted two-cache e2e test",
		IdempotencyKey: idemKey,
	})
	if err != nil {
		lg.fatalf("create live issue: %v", err)
	}
	if writeResult.RemoteNumber <= 0 {
		lg.fatalf("create live issue returned invalid remote number: %d", writeResult.RemoteNumber)
	}

	confirmIssueVisible(ctx, lg, client, owner, repo, writeResult.RemoteNumber)

	snapshotAliases := append([]syncAlias(nil), aliases...)
	snapshotAliases = append(snapshotAliases, syncAlias{RemoteType: "issue", RemoteID: strconv.Itoa(writeResult.RemoteNumber), Number: writeResult.RemoteNumber})
	syncAliases(ctx, lg, svcA, repoIDA, snapshotAliases, "cache A post-write sync")

	svcB, storeB, repoIDB := newService(ctx, t, client, owner, repo, baseURL)
	syncAliases(ctx, lg, svcB, repoIDB, snapshotAliases, "cache B sync")

	validateRemoteIDs(ctx, lg, storeA, repoIDA)
	validateRemoteIDs(ctx, lg, storeB, repoIDB)

	digestA := computeDigest(ctx, lg, storeA, repoIDA)
	digestB := computeDigest(ctx, lg, storeB, repoIDB)
	assertDigestEqual(lg, digestA, digestB)

	createdKey := "issue:" + strconv.Itoa(writeResult.RemoteNumber)
	assertCreatedIssue(lg, digestA, createdKey, "cache A")
	assertCreatedIssue(lg, digestB, createdKey, "cache B")

	countsA, err := storeA.RecordCounts(ctx, repoIDA)
	if err != nil {
		lg.fatalf("cache A record counts: %v", err)
	}
	countsB, err := storeB.RecordCounts(ctx, repoIDB)
	if err != nil {
		lg.fatalf("cache B record counts: %v", err)
	}
	if countsA.Comments != countsB.Comments {
		lg.errorf("comment count mismatch: cacheA=%d cacheB=%d", countsA.Comments, countsB.Comments)
	}
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Skipf("missing required env: %s", name)
	}
	return value
}

func newService(ctx context.Context, t *testing.T, client *gitcode.HTTPClient, owner, repo, baseURL string) (*service.Service, *cache.SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "gitcode-cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatalf("create cache store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repoID := owner + "/" + repo
	if err := store.AddRepository(ctx, cache.RepositoryBinding{
		RepoID:     repoID,
		Owner:      owner,
		Name:       repo,
		APIBaseURL: baseURL,
		Scopes:     []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki},
	}); err != nil {
		t.Fatalf("add repository binding: %v", err)
	}
	return service.NewWithClient(store, client), store, repoID
}

func discoverAliases(ctx context.Context, lg *testLogger, client *gitcode.HTTPClient, owner, repo string) ([]syncAlias, error) {
	var aliases []syncAlias
	issueReq := gitcode.IssueListRequest{Owner: owner, Repo: repo, Page: 1, PerPage: 100}
	for {
		page, err := client.ListIssues(ctx, issueReq)
		if err != nil {
			return nil, err
		}
		for _, issue := range page.Items {
			if issue.Number > 0 {
				aliases = append(aliases, syncAlias{RemoteType: "issue", RemoteID: strconv.Itoa(issue.Number), Number: issue.Number})
			}
		}
		if page.NextPage == 0 || page.NextPage <= issueReq.Page {
			break
		}
		issueReq.Page = page.NextPage
	}

	wikiReq := gitcode.WikiListRequest{Owner: owner, Repo: repo, Page: 1, PerPage: 100}
	for {
		page, err := client.ListWikiPages(ctx, wikiReq)
		if err != nil {
			return nil, err
		}
		for _, wiki := range page.Items {
			if strings.TrimSpace(wiki.Slug) != "" {
				aliases = append(aliases, syncAlias{RemoteType: "wiki", RemoteID: wiki.Slug, Slug: wiki.Slug})
			}
		}
		if page.NextPage == 0 || page.NextPage <= wikiReq.Page {
			break
		}
		wikiReq.Page = page.NextPage
	}
	return aliases, nil
}

func syncAliases(ctx context.Context, lg *testLogger, svc *service.Service, repoID string, aliases []syncAlias, label string) {
	failures := 0
	for _, alias := range aliases {
		result, err := svc.SyncToCache(ctx, service.SyncRequest{RepoID: repoID, RemoteAlias: alias.RemoteType + ":" + alias.RemoteID})
		if err != nil {
			failures++
			lg.errorf("%s failed: type=%s err=%v", label, alias.RemoteType, err)
			continue
		}
		if result.Status != "succeeded" {
			failures++
			lg.errorf("%s returned status=%s type=%s", label, result.Status, alias.RemoteType)
			continue
		}
		lg.logf("%s succeeded: type=%s fetched=%d", label, alias.RemoteType, result.Counts.Fetched)
	}
	if failures > 0 {
		lg.fatalf("%s failures: %d/%d", label, failures, len(aliases))
	}
}

func confirmIssueVisible(ctx context.Context, lg *testLogger, client *gitcode.HTTPClient, owner, repo string, number int) {
	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	attempt := 1
	for {
		_, err := client.GetIssue(pollCtx, gitcode.IssueRequest{Owner: owner, Repo: repo, Number: number})
		if err == nil {
			return
		}
		lg.logf("polling issue #%d visibility attempt %d", number, attempt)
		select {
		case <-pollCtx.Done():
			lg.fatalf("created issue #%d not visible on remote after 30s", number)
		case <-ticker.C:
			attempt++
		}
	}
}

func validateRemoteIDs(ctx context.Context, lg *testLogger, store *cache.SQLiteStore, repoID string) {
	records, err := store.ListRecords(ctx, cache.RecordFilter{RepoID: repoID, Limit: 0})
	if err != nil {
		lg.fatalf("list records for RemoteID validation: %v", err)
	}
	for _, record := range records {
		if strings.TrimSpace(record.RemoteID) == "" {
			lg.fatalf("RemoteID format mismatch: type=%s remote_id=%q", record.Type, record.RemoteID)
		}
		switch record.Type {
		case "issue":
			if _, err := strconv.Atoi(record.RemoteID); err != nil {
				lg.fatalf("RemoteID format mismatch: type=%s remote_id=%q", record.Type, record.RemoteID)
			}
		case "wiki":
			if strings.TrimSpace(record.RemoteID) == "" {
				lg.fatalf("RemoteID format mismatch: type=%s remote_id=%q", record.Type, record.RemoteID)
			}
		}
	}
}

func computeDigest(ctx context.Context, lg *testLogger, store *cache.SQLiteStore, repoID string) map[string]normalizedRecord {
	records, err := store.ListRecords(ctx, cache.RecordFilter{RepoID: repoID, Limit: 0})
	if err != nil {
		lg.fatalf("list records for digest: %v", err)
	}
	digest := make(map[string]normalizedRecord, len(records))
	for _, record := range records {
		key := record.Type + ":" + record.RemoteID
		digest[key] = normalizedRecord{Type: record.Type, Title: record.Title, Body: normalizeBody(record.Body), Status: record.Status}
	}
	return digest
}

func normalizeBody(body string) string {
	return strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
}

func assertDigestEqual(lg *testLogger, a, b map[string]normalizedRecord) {
	for key, recordA := range a {
		recordB, ok := b[key]
		if !ok {
			continue
		}
		if !reflect.DeepEqual(recordA, recordB) {
			lg.errorf("digest mismatch for %s: cacheA={type:%s status:%s title:[REDACTED] body:[REDACTED]} cacheB={type:%s status:%s title:[REDACTED] body:[REDACTED]}", key, recordA.Type, recordA.Status, recordB.Type, recordB.Status)
		}
	}
	if len(a) != len(b) {
		missingA := missingByType(b, a)
		missingB := missingByType(a, b)
		lg.errorf("digest key set size mismatch: cacheA=%d cacheB=%d missing_from_cacheA=%v missing_from_cacheB=%v", len(a), len(b), missingA, missingB)
	}
}

func assertCreatedIssue(lg *testLogger, digest map[string]normalizedRecord, key, label string) {
	record, ok := digest[key]
	if !ok {
		lg.errorf("created issue missing from %s", label)
		return
	}
	if record.Status != "open" && record.Status != "opened" {
		lg.errorf("created issue status mismatch in %s: status=%s", label, record.Status)
	}
}

func missingByType(left, right map[string]normalizedRecord) map[string]int {
	out := map[string]int{}
	for key, record := range left {
		if _, ok := right[key]; !ok {
			out[record.Type]++
		}
	}
	return out
}

func countAliases(aliases []syncAlias, remoteType string) int {
	count := 0
	for _, alias := range aliases {
		if alias.RemoteType == remoteType {
			count++
		}
	}
	return count
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generate random bytes: %v", err)
	}
	return hex.EncodeToString(buf)
}
