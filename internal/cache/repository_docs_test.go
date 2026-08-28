package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRepositoryDocumentMetadataRoundTripAndRevisionIsolation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)

	for _, setID := range []string{"set-a", "set-b"} {
		if err := store.UpsertRepositoryDocRevisionSet(ctx, RepositoryDocRevisionSet{
			RepoID: "fixture-a", ID: setID, GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
			CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed",
			ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetReady, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("UpsertRepositoryDocRevisionSet(%s): %v", setID, err)
		}
	}
	for _, chunk := range []RepositoryDocChunk{
		{RepoID: "fixture-a", ID: "chunk-a", ObjectFormat: "sha1", BlobOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ContentDigest: "content-a", ByteStart: 0, ByteEnd: 11, LineStart: 1, LineEnd: 1, RawSliceDigest: "slice-a", EmbeddingInputDigest: "input-a", ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: now, UpdatedAt: now},
		{RepoID: "fixture-a", ID: "chunk-b", ObjectFormat: "sha1", BlobOID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ContentDigest: "content-b", ByteStart: 0, ByteEnd: 12, LineStart: 1, LineEnd: 1, RawSliceDigest: "slice-b", EmbeddingInputDigest: "input-b", ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.UpsertRepositoryDocChunk(ctx, chunk); err != nil {
			t.Fatalf("UpsertRepositoryDocChunk(%s): %v", chunk.ID, err)
		}
	}
	if err := store.ReplaceRepositoryDocMembership(ctx, "fixture-a", "set-a", []RepositoryDocMembership{{Path: "docs/a.md", ChunkID: "chunk-a", Authority: "git", BlobOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ContentDigest: "content-a"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceRepositoryDocMembership(ctx, "fixture-a", "set-b", []RepositoryDocMembership{{Path: "docs/b.md", ChunkID: "chunk-b", Authority: "git", BlobOID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ContentDigest: "content-b"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepositoryDocVector(ctx, RepositoryDocVector{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: "chunk-a", Vector: []byte{1, 2, 3, 4}, Dimensions: 1, DType: "float32", EmbeddedAt: now}); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.ListRepositoryDocCandidates(ctx, "fixture-a", "set-a", namespace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Path != "docs/a.md" || candidates[0].ChunkID != "chunk-a" || len(candidates[0].Vector) == 0 {
		t.Fatalf("set-a candidates = %#v", candidates)
	}
	candidates, err = store.ListRepositoryDocCandidates(ctx, "fixture-a", "set-b", namespace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Path != "docs/b.md" || len(candidates[0].Vector) != 0 {
		t.Fatalf("set-b candidates = %#v", candidates)
	}
}

func TestRepositoryDocumentCacheDoesNotPersistSourceText(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustAddTestRepo(t, ctx, store, "fixture-a")
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	sentinel := []byte("REPOSITORY_DOC_SOURCE_SENTINEL_7d69f2d1")
	if err := store.UpsertRepositoryDocRevisionSet(ctx, RepositoryDocRevisionSet{RepoID: "fixture-a", ID: "set", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1", CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed", ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetReady}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepositoryDocChunk(ctx, RepositoryDocChunk{RepoID: "fixture-a", ID: "chunk", ObjectFormat: "sha1", BlobOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ContentDigest: digestForRepositoryDocsTest(sentinel), ByteStart: 0, ByteEnd: len(sentinel), LineStart: 1, LineEnd: 1, RawSliceDigest: digestForRepositoryDocsTest(sentinel), EmbeddingInputDigest: digestForRepositoryDocsTest(append([]byte("embed:"), sentinel...)), ChunkPolicyID: "repo-doc-markdown-v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, sentinel) {
		t.Fatal("repository source text was persisted in cache.db")
	}
}

func TestRepositoryDocumentGCSeparatesCommittedAndOverlayRetention(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	for index, spec := range []struct {
		id      string
		overlay string
		state   string
	}{
		{"commit-old", "", RepoDocSetReady}, {"commit-new", "", RepoDocSetReady},
		{"overlay-old", "overlay-a", RepoDocSetReady}, {"overlay-new", "overlay-b", RepoDocSetReady},
		{"building", "", RepoDocSetBuilding}, {"failed-old", "", RepoDocSetBlocked},
	} {
		when := base.Add(time.Duration(index) * time.Hour)
		if spec.id == "failed-old" {
			when = base.Add(-30 * 24 * time.Hour)
		}
		if err := store.UpsertRepositoryDocRevisionSet(ctx, RepositoryDocRevisionSet{
			RepoID: "fixture-a", ID: spec.id, GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
			CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed",
			OverlayDigest: spec.overlay, ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID,
			State: spec.state, CreatedAt: when, UpdatedAt: when,
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.PruneRepositoryDocRevisionSets(ctx, "fixture-a", RepositoryDocRetentionPolicy{
		RetainCommittedPerIdentity: 1, RetainOverlaysPerIdentity: 1, TerminalCutoff: base.Add(-7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RevisionSetsDeleted != 3 {
		t.Fatalf("deleted revision sets = %d, want 3", result.RevisionSetsDeleted)
	}
	sets, err := store.ListRepositoryDocRevisionSets(ctx, RepositoryDocRevisionSetFilter{RepoID: "fixture-a"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, set := range sets {
		got[set.ID] = true
	}
	for _, want := range []string{"commit-new", "overlay-new", "building"} {
		if !got[want] {
			t.Fatalf("retained sets = %v, missing %s", got, want)
		}
	}
}

func TestRepositoryDocumentGCEnforcesVectorByteCeilingAndProtectsCurrentReadySet(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	for index, id := range []string{"oldest", "older", "current"} {
		when := base.Add(time.Duration(index) * time.Hour)
		setID := "set-" + id
		chunkID := "chunk-" + id
		if err := store.UpsertRepositoryDocRevisionSet(ctx, RepositoryDocRevisionSet{
			RepoID: "fixture-a", ID: setID, GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
			CommitOID: fmt.Sprintf("%040d", index+1), PolicyHash: "policy", PolicySource: "committed",
			ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetReady,
			CreatedAt: when, UpdatedAt: when, CompletedAt: when,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertRepositoryDocChunk(ctx, RepositoryDocChunk{
			RepoID: "fixture-a", ID: chunkID, ObjectFormat: "sha1", BlobOID: fmt.Sprintf("%040d", index+11),
			ContentDigest: "content-" + id, ByteEnd: 4, LineStart: 1, LineEnd: 1,
			RawSliceDigest: "slice-" + id, EmbeddingInputDigest: "input-" + id,
			ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: when, UpdatedAt: when,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceRepositoryDocMembership(ctx, "fixture-a", setID, []RepositoryDocMembership{{Path: "docs/" + id + ".md", ChunkID: chunkID, Authority: "git", ContentDigest: "content-" + id}}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertRepositoryDocVector(ctx, RepositoryDocVector{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: chunkID, Vector: []byte{1, 2, 3, 4}, Dimensions: 1, DType: "float32", EmbeddedAt: when}); err != nil {
			t.Fatal(err)
		}
	}

	result, err := store.PruneRepositoryDocRevisionSets(ctx, "fixture-a", RepositoryDocRetentionPolicy{RetainCommittedPerIdentity: 10, MaxVectorBytes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if result.VectorBytesBefore != 12 || result.VectorBytesAfter != 4 || result.RevisionSetsDeleted != 2 || result.VectorsDeleted != 2 || result.ChunksDeleted != 2 {
		t.Fatalf("GC result = %#v", result)
	}
	sets, err := store.ListRepositoryDocRevisionSets(ctx, RepositoryDocRevisionSetFilter{RepoID: "fixture-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || sets[0].ID != "set-current" {
		t.Fatalf("retained sets = %#v", sets)
	}
}

func TestRepositoryDocumentGCExpiresInactiveOverlayAndOrphanBuildingSet(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	for _, set := range []RepositoryDocRevisionSet{
		{RepoID: "fixture-a", ID: "current", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1", CommitOID: fmt.Sprintf("%040d", 1), PolicyHash: "policy", PolicySource: "committed", ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetReady, CreatedAt: now, UpdatedAt: now},
		{RepoID: "fixture-a", ID: "inactive-overlay", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1", CommitOID: fmt.Sprintf("%040d", 1), PolicyHash: "policy", PolicySource: "committed", OverlayDigest: "overlay-old", ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetReady, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour)},
		{RepoID: "fixture-a", ID: "orphan-building", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1", CommitOID: fmt.Sprintf("%040d", 2), PolicyHash: "policy", PolicySource: "committed", ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetBuilding, CreatedAt: now.Add(-8 * 24 * time.Hour), UpdatedAt: now.Add(-8 * 24 * time.Hour)},
	} {
		if err := store.UpsertRepositoryDocRevisionSet(ctx, set); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.PruneRepositoryDocRevisionSets(ctx, "fixture-a", RepositoryDocRetentionPolicy{
		RetainCommittedPerIdentity: 8, OverlayCutoff: now.Add(-24 * time.Hour), TerminalCutoff: now.Add(-7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RevisionSetsDeleted != 2 {
		t.Fatalf("GC result=%+v", result)
	}
	sets, err := store.ListRepositoryDocRevisionSets(ctx, RepositoryDocRevisionSetFilter{RepoID: "fixture-a"})
	if err != nil || len(sets) != 1 || sets[0].ID != "current" {
		t.Fatalf("retained sets=%+v err=%v", sets, err)
	}
}

func TestResolveRepositoryBindingCanonicalizesAlias(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	repo, err := store.GetRepository(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	repo.Aliases = []string{"urandon/sessionless"}
	if err := store.UpdateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveRepositoryBinding(ctx, "urandon/sessionless")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RepoID != "fixture-a" {
		t.Fatalf("canonical repo id = %q, want fixture-a", resolved.RepoID)
	}
}

func mustRepositoryDocsNamespace(t *testing.T, ctx context.Context, store *SQLiteStore) EmbeddingNamespace {
	t.Helper()
	namespace, err := store.UpsertEmbeddingNamespace(ctx, EmbeddingNamespace{EmbeddingNamespaceIdentity: EmbeddingNamespaceIdentity{
		RepoID: "fixture-a", ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", ModelID: "fake-model", ModelRevision: "v1",
		Dimensions: 1, DType: "float32", Normalization: "l2", DocumentInstructionID: "repo-doc-v1", QueryInstructionID: "repo-doc-query-v1",
		ChunkPolicyID: "repo-doc-markdown-v1", LanguagePolicyID: "default", ConfigHash: "repo-doc-config-v1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return namespace
}

func digestForRepositoryDocsTest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
