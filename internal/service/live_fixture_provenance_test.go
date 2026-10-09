package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/testnet"
)

func TestExactLiveIssue42UsesProviderContextNotStableMarker(t *testing.T) {
	for _, providerID := range []int{420042, 42} {
		t.Run(fmt.Sprint(providerID), func(t *testing.T) {
			ctx := context.Background()
			api := testnet.NewExactIssueAPI(t, providerID, 42)
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "provider-issue", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			svc, err := NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "sync.lock")})
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				result, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "provider-issue", RemoteAlias: "issue:42", IdempotencyKey: fmt.Sprintf("exact-42-%d", attempt)})
				if err != nil || result.Status != "succeeded" || result.Record.ID != "ISSUE-42" {
					t.Fatalf("exact live issue 42: result=%+v err=%v", result, err)
				}
				cached, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "provider-issue", ID: "ISSUE-42"})
				if err != nil || cached.StableSourceID != "ISSUE-42" || cached.IssueNumber != 42 || cached.Provenance != "live" {
					t.Fatalf("canonical readback=%+v err=%v", cached, err)
				}
			}
			if api.Details.Load() != 2 || api.Comments.Load() != 1 {
				t.Fatalf("detail=%d comments=%d; repeated exact refresh should reuse fresh comment coverage", api.Details.Load(), api.Comments.Load())
			}
			identity, err := store.ResolveAliasScoped(ctx, "provider-issue", cache.RemoteAlias{Type: "issue", ID: "42"})
			if err != nil || identity.SourceID != "ISSUE-42" {
				t.Fatalf("stable alias=%+v err=%v", identity, err)
			}
			comment, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "provider-issue", ID: "ISSUECOMMENT-42-420043"})
			if err != nil || len(comment.Links) != 1 || comment.Links[0].TargetID != "ISSUE-42" {
				t.Fatalf("comment parent=%+v err=%v", comment, err)
			}
		})
	}
}

func TestLiveGraphDoesNotReserveStableAliasesOrParentNames(t *testing.T) {
	svc := NewWithClient(nil, &fakeGitCodeClient{})
	svc.providerMode = gitcode.ProviderModeLive
	for _, id := range gitcode.FixtureMarkerIDs() {
		graph := cache.SourceGraph{
			Source:     cache.Source{ID: id, Provenance: cache.ProvenanceLive},
			SyncStatus: &cache.SyncStatus{RemoteID: "42"},
			Identities: []cache.Identity{{SourceID: id, AliasType: "legacy", Alias: id, Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}},
			Comments:   []cache.RecordComment{{RecordID: id, CommentID: "420043"}},
		}
		if err := svc.validateLiveSourceGraph(graph); err != nil {
			t.Fatalf("ordinary stable identity %q rejected: %v", id, err)
		}
	}
}

type explicitFixtureGraphClient struct{ gitcode.Client }

func (explicitFixtureGraphClient) FixtureBoundaryMode() string { return gitcode.FixtureBoundaryMode }
func (explicitFixtureGraphClient) FixtureMarkerIDs() []string  { return gitcode.FixtureMarkerIDs() }

func TestLiveGraphRejectsExplicitFixtureContext(t *testing.T) {
	for _, origin := range []string{"provider", "provenance"} {
		t.Run(origin, func(t *testing.T) {
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			client := gitcode.Client(&fakeGitCodeClient{})
			if origin == "provider" {
				client = explicitFixtureGraphClient{client}
			}
			svc := NewWithClient(store, client)
			svc.providerMode = gitcode.ProviderModeLive
			graph := cache.SourceGraph{Source: cache.Source{ID: "ISSUE-100", Kind: "issue"}, SyncStatus: &cache.SyncStatus{RemoteID: "100"}}
			if origin == "provenance" {
				graph.Source.Provenance = cache.ProvenanceFixture
			}
			var failure ErrSyncFailure
			if err := svc.validateLiveSourceGraph(graph); !errors.As(err, &failure) || failure.Mode != "live_graph_invalid" {
				t.Fatalf("fixture %s admitted: %v", origin, err)
			}
		})
	}
}
