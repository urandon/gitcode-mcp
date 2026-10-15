package servicectl

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func syncSelectionJobFixture(t *testing.T, scopes []cache.RepositoryScope) (Manager, *JobManager, string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"short"}, Scopes: scopes}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	return manager, NewJobManager(filepath.Join(root, "jobs.json")), path, identity.UUID
}

func TestSyncSelectionDaemonDefaultAndExplicitAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes []cache.RepositoryScope
		want   []string
	}{
		{"issues", []cache.RepositoryScope{cache.RepositoryScopeIssues}, []string{"issues"}},
		{"wiki", []cache.RepositoryScope{cache.RepositoryScopeWiki}, []string{"wiki"}},
		{"both", []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, []string{"issues", "wiki"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, jobs, path, _ := syncSelectionJobFixture(t, tc.scopes)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if tc.name != "both" {
				_, err := jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "short", CachePath: path, ProviderMode: "fixture", Issues: true, Wiki: true})
				var invalid service.ErrInvalidQuery
				if !errors.As(err, &invalid) || invalid.Field != "scope" || len(jobs.List()) != 0 {
					t.Fatal("unsupported explicit scope was not rejected before enqueue")
				}
			}
			job, err := jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "short", CachePath: path, ProviderMode: "fixture"})
			if err != nil || job.RepoID != "owner/repo" {
				t.Fatal("default canonical admission failed")
			}
			for ctx.Err() == nil {
				observed, _ := jobs.Get(job.ID)
				if observed.Status == JobStatusSucceeded {
					got := []string{}
					for _, collection := range observed.SyncCollections {
						got = append(got, collection.Collection)
					}
					if !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("admitted workflow=%v want=%v", got, tc.want)
					}
					return
				}
				if observed.Status == JobStatusFailed {
					t.Fatalf("fixture job failed: %s", observed.ErrorClass)
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("fixture job did not finish")
		})
	}
}

func TestSyncSelectionDaemonCoalescesResolvedDefaultIntent(t *testing.T) {
	manager, jobs, path, uuid := syncSelectionJobFixture(t, []cache.RepositoryScope{cache.RepositoryScopeIssues})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := jobs.BeginDirectCacheWriter(uuid, "selection-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	first, err := jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "short", CachePath: path, ProviderMode: "fixture"})
	if err != nil {
		t.Fatal("default admission failed")
	}
	for ctx.Err() == nil {
		observed, _ := jobs.Get(first.ID)
		if observed.SyncStage != nil && observed.SyncStage.Phase == SyncStageWaitingCommit {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("fixture did not retain staged intent")
	}
	second, err := jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "owner/repo", CachePath: path, ProviderMode: "fixture", Issues: true})
	if err != nil || second.ID != first.ID || len(jobs.List()) != 1 {
		t.Fatal("resolved default and explicit work did not coalesce")
	}
	release()
	for ctx.Err() == nil {
		observed, _ := jobs.Get(first.ID)
		if observed.Status == JobStatusSucceeded {
			return
		}
		if observed.Status == JobStatusFailed {
			t.Fatalf("staged fixture failed: %s", observed.ErrorClass)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("coalesced fixture did not complete")
}

func TestSyncSelectionDaemonRejectsEmptyEligibleAndMissingBinding(t *testing.T) {
	manager, jobs, path, _ := syncSelectionJobFixture(t, nil)
	_, err := jobs.StartSync(context.Background(), manager, StartSyncJobRequest{RepoID: "owner/repo", CachePath: path, ProviderMode: "fixture"})
	var invalid service.ErrInvalidQuery
	if !errors.As(err, &invalid) || invalid.Field != "scope" || len(jobs.List()) != 0 {
		t.Fatal("empty eligible selection created a job")
	}
	_, err = jobs.StartSync(context.Background(), manager, StartSyncJobRequest{RepoID: "missing", CachePath: path, ProviderMode: "fixture"})
	var unavailable CacheWriterIdentityError
	if !errors.As(err, &unavailable) || unavailable.DiagnosticCode() != "repository_binding_unavailable" || len(jobs.List()) != 0 {
		t.Fatal("missing binding created a job or lost typed authority failure")
	}
}
