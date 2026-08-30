package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		mustSeedRepositoryDocRevisionSet(t, ctx, store, RepositoryDocRevisionSet{
			RepoID: "fixture-a", ID: setID, GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
			CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed",
			ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetBuilding, CreatedAt: now, UpdatedAt: now,
		})
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
	for _, setID := range []string{"set-a", "set-b"} {
		mustSeedRepositoryDocRevisionSet(t, ctx, store, RepositoryDocRevisionSet{
			RepoID: "fixture-a", ID: setID, GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
			CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed",
			ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetReady, CreatedAt: now, UpdatedAt: now,
		})
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

func TestListRepositoryDocPendingVectorIdentitiesExcludesPublishedAndTerminalOrphans(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	now := time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		setID   string
		chunkID string
		state   string
		vector  bool
	}{
		{setID: "set-active", chunkID: "chunk-active", state: RepoDocSetPartial},
		{setID: "set-published", chunkID: "chunk-published", state: RepoDocSetReady, vector: true},
		{setID: "set-terminal", chunkID: "chunk-terminal", state: RepoDocSetSuperseded},
	} {
		set := RepositoryDocRevisionSet{RepoID: "fixture-a", ID: item.setID, GitStoreRef: "git-store", ObjectFormat: "sha1", CommitOID: strings.Repeat("a", 40), PolicyHash: "policy", PolicySource: "committed", ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetBuilding, CreatedAt: now, UpdatedAt: now}
		mustSeedRepositoryDocRevisionSet(t, ctx, store, set)
		if err := store.UpsertRepositoryDocChunk(ctx, RepositoryDocChunk{RepoID: "fixture-a", ID: item.chunkID, ObjectFormat: "sha1", BlobOID: strings.Repeat("b", 40), ContentDigest: item.chunkID, ByteEnd: 4, LineStart: 1, LineEnd: 1, RawSliceDigest: item.chunkID, EmbeddingInputDigest: item.chunkID, ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceRepositoryDocMembership(ctx, "fixture-a", item.setID, []RepositoryDocMembership{{Path: "docs/" + item.chunkID + ".md", ChunkID: item.chunkID, Authority: "git", BlobOID: strings.Repeat("b", 40), ContentDigest: item.chunkID}}); err != nil {
			t.Fatal(err)
		}
		if item.vector {
			if err := store.UpsertRepositoryDocVector(ctx, RepositoryDocVector{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: item.chunkID, Vector: []byte{1, 2, 3, 4}, Dimensions: 1, DType: "float32", EmbeddedAt: now}); err != nil {
				t.Fatal(err)
			}
		}
		set.State = item.state
		mustSeedRepositoryDocRevisionSet(t, ctx, store, set)
	}
	identities, err := store.ListRepositoryDocPendingVectorIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0].RepoID != "fixture-a" || identities[0].NamespaceID != namespace.ID || identities[0].ChunkID != "chunk-active" {
		t.Fatalf("pending identities=%+v", identities)
	}
}

func TestPublishRepositoryDocRevisionSetIsAtomic(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	set := RepositoryDocRevisionSet{
		RepoID: "fixture-a", ID: "set-atomic", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
		CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed",
		ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetBuilding,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.UpsertRepositoryDocRevisionSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepositoryDocChunk(ctx, RepositoryDocChunk{
		RepoID: "fixture-a", ID: "chunk-old", ObjectFormat: "sha1", BlobOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ContentDigest: "content-old", ByteEnd: 8, LineStart: 1, LineEnd: 1, RawSliceDigest: "slice-old",
		EmbeddingInputDigest: "input-old", ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceRepositoryDocMembership(ctx, "fixture-a", set.ID, []RepositoryDocMembership{{Path: "docs/old.md", ChunkID: "chunk-old", Authority: "git", ContentDigest: "content-old"}}); err != nil {
		t.Fatal(err)
	}

	set.State = RepoDocSetReady
	set.EligibleFiles = 1
	set.EligibleChunks = 1
	set.EmbeddedChunks = 1
	set.CompletedAt = now.Add(time.Minute)
	set.UpdatedAt = set.CompletedAt
	err := store.PublishRepositoryDocRevisionSet(ctx, set, []RepositoryDocMembership{{Path: "docs/new.md", ChunkID: "missing-chunk", Authority: "git", ContentDigest: "content-new"}})
	if err == nil {
		t.Fatal("publication with missing chunk succeeded")
	}
	stored, err := store.GetRepositoryDocRevisionSet(ctx, "fixture-a", set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != RepoDocSetBuilding || stored.EligibleChunks != 0 {
		t.Fatalf("revision set escaped rolled-back publication: %#v", stored)
	}
	memberships, err := store.ListRepositoryDocMembership(ctx, "fixture-a", set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 1 || memberships[0].Path != "docs/old.md" {
		t.Fatalf("membership escaped rolled-back publication: %#v", memberships)
	}
}

func TestLoadRepositoryDocSearchSnapshotRequiresPublishedExactNamespace(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	set := RepositoryDocRevisionSet{
		RepoID: "fixture-a", ID: "set-snapshot", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
		CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed",
		ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetBuilding,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.UpsertRepositoryDocRevisionSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRepositoryDocSearchSnapshot(ctx, "fixture-a", set.ID, namespace.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("building snapshot error = %v, want ErrNotFound", err)
	}
	if err := store.UpsertRepositoryDocChunk(ctx, RepositoryDocChunk{
		RepoID: "fixture-a", ID: "chunk-snapshot", ObjectFormat: "sha1", BlobOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ContentDigest: "content", ByteEnd: 8, LineStart: 1, LineEnd: 1, RawSliceDigest: "slice",
		EmbeddingInputDigest: "input", ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	set.State = RepoDocSetPartial
	set.EligibleFiles = 1
	set.EligibleChunks = 1
	if err := store.PublishRepositoryDocRevisionSet(ctx, set, []RepositoryDocMembership{{Path: "docs/a.md", ChunkID: "chunk-snapshot", Authority: "git", ContentDigest: "content"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRepositoryDocSearchSnapshot(ctx, "fixture-a", set.ID, namespace.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial snapshot error = %v, want ErrNotFound", err)
	}
	if _, err := store.LoadRepositoryDocSearchSnapshot(ctx, "fixture-a", set.ID, "wrong-namespace"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong namespace error = %v, want ErrNotFound", err)
	}
}

func TestRepositoryDocVectorIdentityIsImmutable(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	now := time.Date(2026, 8, 29, 10, 15, 0, 0, time.UTC)
	if err := store.UpsertRepositoryDocChunk(ctx, RepositoryDocChunk{RepoID: "fixture-a", ID: "chunk-immutable", ObjectFormat: "sha1", BlobOID: strings.Repeat("a", 40), ContentDigest: "content", ByteEnd: 4, LineStart: 1, LineEnd: 1, RawSliceDigest: "slice", EmbeddingInputDigest: "input", ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	first := RepositoryDocVector{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: "chunk-immutable", Vector: []byte{1, 2, 3, 4}, Dimensions: 1, DType: "float32", EmbeddedAt: now}
	if err := store.UpsertRepositoryDocVector(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepositoryDocVector(ctx, first); err != nil {
		t.Fatalf("idempotent vector replay: %v", err)
	}
	conflicting := first
	conflicting.Vector = []byte{4, 3, 2, 1}
	conflicting.VectorHash = ""
	if err := store.UpsertRepositoryDocVector(ctx, conflicting); !errors.Is(err, ErrRepositoryDocVectorConflict) {
		t.Fatalf("conflicting vector error = %v, want ErrRepositoryDocVectorConflict", err)
	}
}

func TestReadyRepositoryDocPublicationRejectsInvalidVectorContract(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	now := time.Date(2026, 8, 29, 10, 20, 0, 0, time.UTC)
	set := RepositoryDocRevisionSet{RepoID: "fixture-a", ID: "set-invalid-vector", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1", CommitOID: strings.Repeat("1", 40), PolicyHash: "policy", PolicySource: "committed", ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetBuilding, CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRepositoryDocRevisionSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	chunk := RepositoryDocChunk{RepoID: "fixture-a", ID: "chunk-invalid-vector", ObjectFormat: "sha1", BlobOID: strings.Repeat("a", 40), ContentDigest: "content", ByteEnd: 8, LineStart: 1, LineEnd: 1, RawSliceDigest: "slice", EmbeddingInputDigest: "input", ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRepositoryDocChunk(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceRepositoryDocMembership(ctx, "fixture-a", set.ID, []RepositoryDocMembership{{Path: "docs/a.md", ChunkID: chunk.ID, Authority: "git", ContentDigest: "content"}}); err != nil {
		t.Fatal(err)
	}
	// Namespace dimension is one, but this row claims two dimensions and has
	// only four bytes. Existence alone must never make a set ready.
	if err := store.UpsertRepositoryDocVector(ctx, RepositoryDocVector{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: chunk.ID, Vector: []byte{1, 2, 3, 4}, Dimensions: 2, DType: "float32", EmbeddedAt: now}); err != nil {
		t.Fatal(err)
	}
	set.State = RepoDocSetReady
	set.EligibleFiles = 1
	set.EligibleChunks = 1
	set.EmbeddedChunks = 1
	if err := store.PublishStagedRepositoryDocRevisionSet(ctx, set); err == nil || !strings.Contains(err.Error(), "vector violates namespace contract") {
		t.Fatalf("publication error = %v, want vector contract rejection", err)
	}
}

func TestPublishedRepositoryDocRevisionSetCannotRegressAcrossWriters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	winner, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer winner.Close()
	mustAddTestRepo(t, ctx, winner, "fixture-a")
	namespace := mustRepositoryDocsNamespace(t, ctx, winner)
	stale, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	now := time.Date(2026, 8, 29, 10, 30, 0, 0, time.UTC)
	set := RepositoryDocRevisionSet{
		RepoID: "fixture-a", ID: "set-race", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
		CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed",
		ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetBuilding,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := winner.UpsertRepositoryDocRevisionSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := winner.UpsertRepositoryDocChunk(ctx, RepositoryDocChunk{
		RepoID: "fixture-a", ID: "chunk-ready", ObjectFormat: "sha1", BlobOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ContentDigest: "content", ByteEnd: 8, LineStart: 1, LineEnd: 1, RawSliceDigest: "slice",
		EmbeddingInputDigest: "input", ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := winner.UpsertRepositoryDocVector(ctx, RepositoryDocVector{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: "chunk-ready", Vector: []byte{1, 2, 3, 4}, Dimensions: 1, DType: "float32", EmbeddedAt: now}); err != nil {
		t.Fatal(err)
	}
	membership := []RepositoryDocMembership{{Path: "docs/ready.md", ChunkID: "chunk-ready", Authority: "git", ContentDigest: "content"}}
	ready := set
	ready.State = RepoDocSetReady
	ready.EligibleFiles = 1
	ready.EligibleChunks = 1
	ready.EmbeddedChunks = 1
	ready.CompletedAt = now.Add(time.Second)
	ready.UpdatedAt = ready.CompletedAt
	if err := winner.PublishRepositoryDocRevisionSet(ctx, ready, membership); err != nil {
		t.Fatal(err)
	}

	staleBuilding := set
	staleBuilding.UpdatedAt = now.Add(2 * time.Second)
	if err := stale.UpsertRepositoryDocRevisionSet(ctx, staleBuilding); !errors.Is(err, ErrRepositoryDocRevisionSetPublished) {
		t.Fatalf("stale building update error = %v, want ErrRepositoryDocRevisionSetPublished", err)
	}
	stalePartial := set
	stalePartial.State = RepoDocSetPartial
	stalePartial.EligibleFiles = 1
	stalePartial.EligibleChunks = 1
	stalePartial.FailedChunks = 1
	stalePartial.UpdatedAt = now.Add(3 * time.Second)
	if err := stale.PublishRepositoryDocRevisionSet(ctx, stalePartial, membership); !errors.Is(err, ErrRepositoryDocRevisionSetPublished) {
		t.Fatalf("stale partial publication error = %v, want ErrRepositoryDocRevisionSetPublished", err)
	}
	if err := stale.UpsertRepositoryDocMembership(ctx, RepositoryDocMembership{RepoID: "fixture-a", RevisionSetID: set.ID, Path: "docs/stale.md", ChunkID: "chunk-ready", Authority: "git", ContentDigest: "content"}); !errors.Is(err, ErrRepositoryDocRevisionSetPublished) {
		t.Fatalf("stale membership update error = %v, want ErrRepositoryDocRevisionSetPublished", err)
	}
	if err := stale.ReplaceRepositoryDocMembership(ctx, "fixture-a", set.ID, membership); !errors.Is(err, ErrRepositoryDocRevisionSetPublished) {
		t.Fatalf("stale membership replacement error = %v, want ErrRepositoryDocRevisionSetPublished", err)
	}
	if err := stale.UpsertRepositoryDocExclusion(ctx, RepositoryDocExclusion{RepoID: "fixture-a", RevisionSetID: set.ID, Path: "docs/stale.md", Authority: "git", ReasonCode: "nul_content"}); !errors.Is(err, ErrRepositoryDocRevisionSetPublished) {
		t.Fatalf("stale exclusion update error = %v, want ErrRepositoryDocRevisionSetPublished", err)
	}
	stored, err := stale.GetRepositoryDocRevisionSet(ctx, "fixture-a", set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != RepoDocSetReady || stored.EmbeddedChunks != 1 || stored.FailedChunks != 0 || !stored.CompletedAt.Equal(ready.CompletedAt) {
		t.Fatalf("published set regressed: %#v", stored)
	}
	storedMembership, err := stale.ListRepositoryDocMembership(ctx, "fixture-a", set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedMembership) != 1 || storedMembership[0].Path != "docs/ready.md" {
		t.Fatalf("published membership regressed: %#v", storedMembership)
	}
}

func TestReadyRepositoryDocPublicationRequiresExactCoverageAndVectors(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	namespace := mustRepositoryDocsNamespace(t, ctx, store)
	now := time.Date(2026, 8, 29, 10, 45, 0, 0, time.UTC)
	set := RepositoryDocRevisionSet{
		RepoID: "fixture-a", ID: "set-coverage", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
		CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed",
		ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetBuilding, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.UpsertRepositoryDocRevisionSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepositoryDocChunk(ctx, RepositoryDocChunk{RepoID: "fixture-a", ID: "chunk", ObjectFormat: "sha1", BlobOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ContentDigest: "content", ByteEnd: 8, LineStart: 1, LineEnd: 1, RawSliceDigest: "slice", EmbeddingInputDigest: "input", ChunkPolicyID: "repo-doc-markdown-v1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	set.State = RepoDocSetReady
	set.EligibleChunks = 1
	set.EmbeddedChunks = 1
	membership := []RepositoryDocMembership{{Path: "docs/a.md", ChunkID: "chunk", Authority: "git", ContentDigest: "content"}}
	if err := store.PublishRepositoryDocRevisionSet(ctx, set, membership); err == nil || !strings.Contains(err.Error(), "exact-namespace vector") {
		t.Fatalf("ready publication without vector error = %v", err)
	}
	if err := store.UpsertRepositoryDocVector(ctx, RepositoryDocVector{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: "chunk", Vector: []byte{1, 2, 3, 4}, Dimensions: 1, DType: "float32", EmbeddedAt: now}); err != nil {
		t.Fatal(err)
	}
	set.FailedChunks = 1
	if err := store.PublishRepositoryDocRevisionSet(ctx, set, membership); err == nil || !strings.Contains(err.Error(), "incomplete coverage") {
		t.Fatalf("ready publication with failures error = %v", err)
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
	mustSeedRepositoryDocRevisionSet(t, ctx, store, RepositoryDocRevisionSet{RepoID: "fixture-a", ID: "set", GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1", CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed", ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetReady})
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
		mustSeedRepositoryDocRevisionSet(t, ctx, store, RepositoryDocRevisionSet{
			RepoID: "fixture-a", ID: spec.id, GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
			CommitOID: "0123456789012345678901234567890123456789", PolicyHash: "policy", PolicySource: "committed",
			OverlayDigest: spec.overlay, ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID,
			State: spec.state, CreatedAt: when, UpdatedAt: when,
		})
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
		mustSeedRepositoryDocRevisionSet(t, ctx, store, RepositoryDocRevisionSet{
			RepoID: "fixture-a", ID: setID, GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
			CommitOID: fmt.Sprintf("%040d", index+1), PolicyHash: "policy", PolicySource: "committed",
			ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetBuilding,
			CreatedAt: when, UpdatedAt: when, CompletedAt: when,
		})
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
		mustSeedRepositoryDocRevisionSet(t, ctx, store, RepositoryDocRevisionSet{
			RepoID: "fixture-a", ID: setID, GitStoreRef: "git-store:fixture-a", ObjectFormat: "sha1",
			CommitOID: fmt.Sprintf("%040d", index+1), PolicyHash: "policy", PolicySource: "committed",
			ChunkPolicyID: "repo-doc-markdown-v1", NamespaceID: namespace.ID, State: RepoDocSetReady,
			CreatedAt: when, UpdatedAt: when, CompletedAt: when,
		})
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
		mustSeedRepositoryDocRevisionSet(t, ctx, store, set)
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

func mustSeedRepositoryDocRevisionSet(t *testing.T, ctx context.Context, store *SQLiteStore, set RepositoryDocRevisionSet) {
	t.Helper()
	normalized, err := normalizeRepositoryDocRevisionSet(set)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertRepositoryDocRevisionSet(ctx, store.db, normalized); err != nil {
		t.Fatal(err)
	}
}
