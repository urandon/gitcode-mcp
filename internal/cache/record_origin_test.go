package cache

import (
	"context"
	"testing"
)

func TestRecordGraphSourceOriginIsIndependentOfStorageRole(t *testing.T) {
	for _, origin := range []Provenance{ProvenanceLive, ProvenanceFixture, ""} {
		t.Run(string(origin), func(t *testing.T) {
			ctx := context.Background()
			store, err := NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "origin-test", Owner: "owner", Name: "repo"}); err != nil {
				t.Fatal(err)
			}
			primary := Record{RepoID: "origin-test", ID: "ISSUE-7", Type: "issue", Path: "issues/7.md", Title: "Origin primary", Body: "needle", Provenance: ProvenanceRemote}
			related := Record{RepoID: "origin-test", ID: "MILESTONE-1", Type: "milestone", Path: "milestones/1.md", Title: "Origin related", Body: "needle", Provenance: ProvenanceRemote}
			if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: primary, RelatedRecords: []Record{related}, SourceProvenance: origin}); err != nil {
				t.Fatal(err)
			}
			want := origin
			if want == "" {
				want = ProvenanceFixture
			}
			for _, id := range []string{primary.ID, related.ID} {
				source, err := store.GetSourceScoped(ctx, "origin-test", id)
				if err != nil || source.Provenance != want {
					t.Fatalf("source %s origin=%s err=%v", id, source.Provenance, err)
				}
				record, err := store.GetRecord(ctx, "origin-test", id)
				if err != nil || record.Provenance != ProvenanceRemote {
					t.Fatalf("record %s role=%s err=%v", id, record.Provenance, err)
				}
			}
			query := SearchQuery{RepoID: "origin-test", Query: "needle", Limit: 10}
			query.SetProvenance(want)
			results, err := store.SearchSources(ctx, query)
			if err != nil || len(results) != 2 {
				t.Fatalf("origin-filtered search count=%d err=%v", len(results), err)
			}
		})
	}
}
