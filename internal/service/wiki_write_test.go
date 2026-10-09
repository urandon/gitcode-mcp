package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

type wikiConfirmationClient struct {
	*fakeGitCodeClient
	writes, reads     int
	writeErr, readErr error
	page              gitcode.WikiPage
	onWrite           func()
}

func (c *wikiConfirmationClient) write() (gitcode.WriteResult[gitcode.WikiPage], error) {
	c.writes++
	if c.onWrite != nil {
		c.onWrite()
	}
	if c.writeErr != nil {
		return gitcode.WriteResult[gitcode.WikiPage]{}, c.writeErr
	}
	return gitcode.WriteResult[gitcode.WikiPage]{Record: c.page, Confirmed: true, RemoteID: c.page.Slug, RemoteSlug: c.page.Slug, RemoteRevision: c.page.Revision}, nil
}
func (c *wikiConfirmationClient) CreateWikiPage(context.Context, gitcode.CreateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return c.write()
}
func (c *wikiConfirmationClient) UpdateWikiPage(context.Context, gitcode.UpdateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return c.write()
}
func (c *wikiConfirmationClient) GetWikiPage(_ context.Context, req gitcode.WikiPageRequest) (gitcode.WikiPage, error) {
	c.reads++
	if req.Slug != "Home.md" {
		return gitcode.WikiPage{}, errors.New("wrong exact path")
	}
	return c.page, c.readErr
}

type wikiFaultStore struct {
	*cache.SQLiteStore
	claimErr, graphErr error
}

func (s *wikiFaultStore) ClaimAuditEventGeneration(ctx context.Context, e cache.AuditTrailEntry, previous *time.Time) (bool, error) {
	if s.claimErr != nil {
		return false, s.claimErr
	}
	return s.SQLiteStore.ClaimAuditEventGeneration(ctx, e, previous)
}
func (s *wikiFaultStore) UpsertRecordGraph(ctx context.Context, g cache.RecordGraph) error {
	if s.graphErr != nil {
		return s.graphErr
	}
	return s.SQLiteStore.UpsertRecordGraph(ctx, g)
}

func TestWikiWriteDurableClaimAndReadOnlyRecovery(t *testing.T) {
	for _, command := range []string{"create-page", "update-page"} {
		t.Run(command, func(t *testing.T) {
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedStore(t, ctx, store)
			t.Setenv("GITCODE_TOKEN", "test-token")
			client := &wikiConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{}, page: gitcode.WikiPage{ID: "Home.md", Slug: "Home.md", Body: "wanted", Revision: "rev"}, writeErr: gitcode.ErrWriteMutationPhase{Phase: "readback", MutationAttempted: true, Cause: &gitcode.ErrSchemaDecode{Field: "content", Message: "opaque-private-provider-body"}}}
			client.onWrite = func() {
				e, err := store.GetAuditEventByKey(ctx, "fixture-a", "wiki-key")
				if err != nil || e == nil || e.Status != audit.StatusInProgress {
					t.Fatal("mutation preceded durable claim")
				}
			}
			req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Path: "Home.md", Body: "wanted", IdempotencyKey: "wiki-key"}
			_, err = NewWithClient(store, client).executeWrite(ctx, command, req, RepositoryScopeWiki)
			var failure ErrWriteFailure
			if !errors.As(err, &failure) || failure.WritePhase != "readback" || failure.MutationAttempted == nil || !*failure.MutationAttempted || strings.Contains(err.Error(), "opaque-private") {
				t.Fatalf("unsafe failure: %v", err)
			}
			// A new service instance represents restart. Mismatched or unavailable
			// readback cannot clear the claim or emit another mutation.
			client.page.Body = "different"
			_, err = NewWithClient(store, client).executeWrite(ctx, command, req, RepositoryScopeWiki)
			if err == nil || client.writes != 1 {
				t.Fatal("mismatched readback repeated mutation")
			}
			client.readErr = errors.New("private readback detail")
			_, err = NewWithClient(store, client).executeWrite(ctx, command, req, RepositoryScopeWiki)
			if err == nil || strings.Contains(err.Error(), "private readback") || client.writes != 1 {
				t.Fatal("unsafe failed readback")
			}
			client.page.Body, client.readErr = "wanted", nil
			result, err := NewWithClient(store, client).executeWrite(ctx, command, req, RepositoryScopeWiki)
			if err != nil || result.Status != "recovered_after_ambiguous_write" || !result.Replayed || client.writes != 1 {
				t.Fatalf("recovery: %+v %v writes=%d", result, err, client.writes)
			}
			replayed, err := NewWithClient(store, client).executeWrite(ctx, command, req, RepositoryScopeWiki)
			if err != nil || !replayed.Replayed || replayed.RemoteRevision != "rev" || client.reads != 3 || client.writes != 1 {
				t.Fatalf("replay: %+v %v reads=%d", replayed, err, client.reads)
			}
			req.Body = "new intent"
			_, err = NewWithClient(store, client).executeWrite(ctx, command, req, RepositoryScopeWiki)
			if !errors.As(err, &failure) || failure.Code != "write_idempotency_conflict" || client.writes != 1 {
				t.Fatal("conflicting intent was not blocked")
			}
		})
	}
}

func TestWikiWriteClaimAndCacheFailures(t *testing.T) {
	for _, phase := range []string{"claim", "cache"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedStore(t, ctx, store)
			t.Setenv("GITCODE_TOKEN", "test-token")
			fault := &wikiFaultStore{SQLiteStore: store}
			if phase == "claim" {
				fault.claimErr = errors.New("injected")
			} else {
				fault.graphErr = errors.New("injected")
			}
			client := &wikiConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{}, page: gitcode.WikiPage{Slug: "Home.md", Body: "wanted", Revision: "rev"}}
			req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Path: "Home.md", Body: "wanted", IdempotencyKey: "wiki-key"}
			_, err = NewWithClient(fault, client).CreatePage(ctx, req)
			if err == nil {
				t.Fatal("fault ignored")
			}
			if phase == "claim" {
				if client.writes != 0 {
					t.Fatal("mutation without claim")
				}
				return
			}
			fault.graphErr = nil
			result, err := NewWithClient(fault, client).CreatePage(ctx, req)
			if err != nil || !result.Replayed || client.writes != 1 || client.reads != 1 {
				t.Fatalf("cache recovery repeated mutation: %+v %v", result, err)
			}
		})
	}
}

func TestWikiWriteLateFailureCannotDowngradeRecoveredGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	t.Setenv("GITCODE_TOKEN", "test-token")
	client := &wikiConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{}, page: gitcode.WikiPage{Slug: "Home.md", Body: "wanted", Revision: "rev"}, writeErr: gitcode.ErrWriteMutationPhase{Phase: "readback", MutationAttempted: true, Cause: errors.New("late transport")}}
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Path: "Home.md", Body: "wanted", IdempotencyKey: "wiki-key"}
	client.onWrite = func() {
		result, err := NewWithClient(store, client).CreatePage(ctx, req)
		if err != nil || !result.Replayed {
			t.Fatalf("interleaved recovery failed: %+v %v", result, err)
		}
	}
	result, err := NewWithClient(store, client).CreatePage(ctx, req)
	entry, auditErr := store.GetAuditEventByKey(ctx, "fixture-a", "wiki-key")
	if err != nil || !result.Replayed || auditErr != nil || entry == nil || entry.Status != audit.StatusSucceeded || client.writes != 1 {
		t.Fatalf("late failure downgraded recovery: %+v %v", result, err)
	}
}

func TestWikiWriteLegacyFailedReceiptIsReadOnly(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	t.Setenv("GITCODE_TOKEN", "test-token")
	client := &wikiConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{}, page: gitcode.WikiPage{Slug: "Home.md", Body: "wanted", Revision: "rev"}, writeErr: gitcode.ErrWriteMutationPhase{Phase: "readback", MutationAttempted: true}}
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Path: "Home.md", Body: "wanted", IdempotencyKey: "wiki-key"}
	_, _ = NewWithClient(store, client).UpdatePage(ctx, req)
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "wiki-key")
	if err != nil || entry == nil {
		t.Fatal("missing claim")
	}
	entry.Status = audit.StatusFailed
	if err := store.RecordAuditEvent(ctx, *entry); err != nil {
		t.Fatal(err)
	}
	client.writes = 0
	result, err := NewWithClient(store, client).UpdatePage(ctx, req)
	if err != nil || !result.Replayed || client.writes != 0 || client.reads != 1 {
		t.Fatalf("legacy failure reissued PUT: %+v %v", result, err)
	}
}

func TestWikiWriteRecoveryPreservesAttemptEvidence(t *testing.T) {
	for _, flag := range []string{"true", "false", "unknown", ""} {
		failure := wikiRecoveredFailure(cache.AuditTrailEntry{RepoID: "fixture-a", IdempotencyKey: "key", RequestMetadata: map[string]string{"mutation_attempted": flag}}, "Home.md", "write_ambiguous_remote", "readback", nil)
		if flag == "true" || flag == "false" {
			if failure.MutationAttempted == nil || *failure.MutationAttempted != (flag == "true") {
				t.Fatal("known transport evidence changed")
			}
		} else if failure.MutationAttempted != nil {
			t.Fatal("unknown transport was falsely asserted")
		}
	}
}
