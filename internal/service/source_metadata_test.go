package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gitcode-mcp/internal/rag"
)

func TestSourceMetadataQueryPaths(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	calls := 0
	svc.SetRAGSearchProvider(func(_ context.Context, req rag.SearchRequest) (rag.SearchResult, error) {
		calls++
		if !reflect.DeepEqual(req.SourceIDs, []string{"DOC-123"}) {
			t.Fatalf("eligibility: %#v", req.SourceIDs)
		}
		return rag.SearchResult{Status: rag.RAGSearchStatusReady, Namespace: rag.NamespaceStatus{ID: "n", Exists: true, Current: true}, Results: []rag.SearchContext{
			{ChunkID: "eligible", SourceID: "DOC-123", Score: rag.ScoreBreakdown{Semantic: 0.8}},
			{ChunkID: "excluded", SourceID: "TASK-001", Score: rag.ScoreBreakdown{Semantic: 0.99}},
		}}, nil
	})
	got, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Labels: []string{"design"}, TitleContains: "Design", Limit: 1})
	if err != nil || len(got.Results) != 1 || got.Results[0].ID != "DOC-123" || calls != 0 || got.RAGState != "not_requested" || got.Filters.Scope != "cached_sources" {
		t.Fatalf("metadata: %v %#v calls=%d", err, got, calls)
	}
	got, err = svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "semantic", Labels: []string{"design"}, Limit: 1})
	if err != nil || len(got.Results) != 1 || got.Results[0].ID != "DOC-123" || calls != 1 {
		t.Fatalf("fusion: %v %#v", err, got)
	}
	got, err = svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "TASK-001", Labels: []string{"design"}, Mode: "full_text"})
	if err != nil || len(got.Results) != 0 {
		t.Fatalf("exact-id bypass: %v %#v", err, got)
	}
	list, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a", Labels: []string{"missing"}})
	if err != nil || len(list.Results) != 0 || list.Results == nil || list.Filters.LabelEvidence != "positive_membership_only_unknown_excluded" {
		t.Fatalf("empty: %v %#v", err, list)
	}
	list, err = svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a", Labels: []string{"design", "task"}, LabelsMatch: "any", Limit: 1, Offset: 1})
	if err != nil || len(list.Results) != 1 || list.Results[0].ID != "TASK-001" {
		t.Fatalf("paged: %v %#v", err, list)
	}
	for _, req := range []ListSourcesRequest{{Labels: []string{" "}}, {Labels: []string{"a", "a"}}, {LabelsMatch: "or"}} {
		req.RepoID = "fixture-a"
		_, err := svc.ListSources(ctx, req)
		var invalid ErrInvalidQuery
		if !errors.As(err, &invalid) {
			t.Fatalf("validation: %v", err)
		}
	}
	svc.SetRAGSearchProvider(func(context.Context, rag.SearchRequest) (rag.SearchResult, error) {
		return rag.SearchResult{}, &rag.ProviderError{Class: rag.ProviderFailureUnavailable}
	})
	got, err = svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog", Status: "ready", Labels: []string{"design"}})
	if err != nil || got.FallbackReason != "provider_unavailable" || len(got.Results) != 1 || got.Results[0].ID != "DOC-123" || got.Filters.Status != "ready" {
		t.Fatalf("fallback dropped predicate: %v %#v", err, got)
	}
	svc.SetRAGSearchProvider(func(context.Context, rag.SearchRequest) (rag.SearchResult, error) {
		t.Fatal("empty metadata cohort called RAG")
		return rag.SearchResult{}, nil
	})
	got, err = svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog", Labels: []string{"missing"}})
	if err != nil || len(got.Results) != 0 {
		t.Fatalf("empty eligibility: %v %#v", err, got)
	}
}
