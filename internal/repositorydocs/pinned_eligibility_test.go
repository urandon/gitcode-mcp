package repositorydocs

import (
	"context"
	"testing"

	"gitcode-mcp/internal/cache"
)

type ineligibleVectorStore struct {
	SearchStore
	excluded string
}

func (s ineligibleVectorStore) LoadRepositoryDocSearchSnapshot(ctx context.Context, repo, set, namespace string) (cache.RepositoryDocSearchSnapshot, error) {
	snapshot, err := s.SearchStore.LoadRepositoryDocSearchSnapshot(ctx, repo, set, namespace)
	for i := range snapshot.Candidates {
		if snapshot.Candidates[i].Path == s.excluded {
			snapshot.Candidates[i].Vector = nil
		}
	}
	return snapshot, err
}

func TestPinnedEligibilityPrecedesSemanticScoring(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/eligible.md", "needle eligible\n")
	writeTestFile(t, root, "docs/excluded.md", "needle excluded\n")
	commit := commitTestRepository(t, root, "eligibility fixture")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal("Git fixture unavailable")
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := repositoryDocsSearchProvider(t)
	indexed, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	if err != nil {
		t.Fatal("fixture index unavailable")
	}
	set, err := store.GetRepositoryDocRevisionSet(ctx, "owner/repo", indexed.RevisionSetID)
	if err != nil {
		t.Fatal("fixture set unavailable")
	}
	namespace, err := store.GetEmbeddingNamespace(ctx, "owner/repo", indexed.NamespaceID)
	if err != nil {
		t.Fatal("fixture namespace unavailable")
	}
	snapshot, err := store.LoadRepositoryDocSearchSnapshot(ctx, "owner/repo", indexed.RevisionSetID, indexed.NamespaceID)
	if err != nil {
		t.Fatal("fixture candidates unavailable")
	}
	eligible := map[string]string{}
	for _, candidate := range snapshot.Candidates {
		if candidate.Path == "docs/eligible.md" {
			eligible[candidate.Path] = candidate.BlobOID
		}
	}
	if len(eligible) != 1 {
		t.Fatal("eligible fixture missing")
	}
	retriever := NewRetriever(ineligibleVectorStore{SearchStore: store, excluded: "docs/excluded.md"}, provider)
	q := SearchRequest{RepoID: "owner/repo", Query: "needle", Repository: repo, EligiblePinnedPaths: eligible}
	hits, err := retriever.semantic(ctx, q, set, namespace.EmbeddingNamespaceIdentity)
	if err != nil || len(hits) != 1 {
		t.Fatal("ineligible candidate reached vector scoring")
	}
	for _, hit := range hits {
		if hit.candidate.Path != "docs/eligible.md" {
			t.Fatal("ineligible semantic rank published")
		}
	}
	q.EligiblePinnedPaths = nil
	if _, err := retriever.semantic(ctx, q, set, namespace.EmbeddingNamespaceIdentity); err == nil {
		t.Fatal("unrestricted bad-vector control did not fail")
	}
	q.EligiblePinnedPaths = map[string]string{"docs/eligible.md": "wrong-blob"}
	if _, err := retriever.semantic(ctx, q, set, namespace.EmbeddingNamespaceIdentity); err == nil {
		t.Fatal("eligible path accepted a different blob")
	}
}
