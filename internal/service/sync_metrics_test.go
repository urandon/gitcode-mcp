package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/observability"
)

func syncMetricContext() (context.Context, *observability.Metrics) {
	metrics := observability.NewMetrics(strings.Repeat("a", 32), time.Now().UTC())
	return observability.WithMetrics(context.Background(), metrics), metrics
}

func requireSyncCounter(t *testing.T, metrics *observability.Metrics, collection string, want float64) {
	t.Helper()
	for _, series := range metrics.Snapshot().Series {
		if series.Name == "sync_committed" && series.Labels["collection"] == collection {
			if series.Value == nil || *series.Value != want {
				t.Fatal("confirmed graph throughput differs")
			}
			return
		}
	}
	t.Fatal("fixed throughput series missing")
}

type metricFaultSyncStore struct {
	*cache.SQLiteStore
	t                 *testing.T
	metrics           *observability.Metrics
	failure           error
	commits, releases int
	beforeRelease     float64
}

func (s *metricFaultSyncStore) CommitSyncBatch(ctx context.Context, batch cache.SyncBatch) error {
	s.commits++
	if s.failure != nil {
		return s.failure
	}
	return s.SQLiteStore.CommitSyncBatch(ctx, batch)
}

func (s *metricFaultSyncStore) ReleaseWriter(ctx context.Context, lease *cache.WriterLease) error {
	requireSyncCounter(s.t, s.metrics, "issues", s.beforeRelease)
	err := s.SQLiteStore.ReleaseWriter(ctx, lease)
	s.releases++
	return err
}

func TestSyncThroughputRequiresAtomicCommitAndWriterRelease(t *testing.T) {
	ctx, metrics := syncMetricContext()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal("cache fixture failed")
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal("binding fixture failed")
	}
	failure := errors.New("synthetic atomic commit failure")
	faults := &metricFaultSyncStore{SQLiteStore: store, t: t, metrics: metrics, failure: failure}
	svc := NewWithClient(faults, &fakeGitCodeClient{})
	batch := DurableIssueSyncBatch{Version: DurableSyncBatchVersion, RepoID: "fixture-a", Collection: "issues", IdempotencyKey: "metric-test", Items: []DurableIssueItem{{Number: 1, Title: "Fixture"}}, StopReason: "provider_failure", TraversalStatus: "partial", RecordsListed: 2}
	if _, err := svc.CommitIssueSyncBatch(ctx, batch, nil); !errors.Is(err, failure) {
		// Service may normalize the persistence error into a typed sync error.
		if err == nil {
			t.Fatal("atomic failure lost")
		}
	}
	requireSyncCounter(t, metrics, "issues", 0)
	if _, err := store.GetSourceScoped(ctx, "fixture-a", "ISSUE-1"); err == nil {
		t.Fatal("failed commit published source")
	}
	faults.failure = nil
	result, err := svc.CommitIssueSyncBatch(ctx, batch, nil)
	if err != nil || result.SuccessCount != 1 || result.TraversalStatus != "partial" {
		t.Fatal("partial confirmed batch changed")
	}
	requireSyncCounter(t, metrics, "issues", 1)
	if faults.commits != 2 || faults.releases != 2 {
		t.Fatal("fault retry changed commit/lease boundaries")
	}
	// Cancellation before acquisition changes no execution bytes or throughput.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.CommitIssueSyncBatch(cancelled, batch, nil); err == nil {
		t.Fatal("cancelled commit accepted")
	}
	requireSyncCounter(t, metrics, "issues", 1)
}
