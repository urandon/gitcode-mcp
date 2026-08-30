package repositorydocs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileVectorCheckpointStoreRecoversFromCorruption(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileVectorCheckpointStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.checkpointPath("owner/repo", "namespace", "chunk")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "owner/repo", "namespace", "chunk"); !errors.Is(err, ErrVectorCheckpointNotFound) {
		t.Fatalf("corrupt checkpoint error = %v, want recoverable not found", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt checkpoint remains: %v", err)
	}
}

func TestFileVectorCheckpointStorePrunesOrphansAgeAndBytes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileVectorCheckpointStore(root)
	if err != nil {
		t.Fatal(err)
	}
	save := func(chunk string) string {
		t.Helper()
		if err := store.Save(ctx, VectorCheckpoint{RepoID: "owner/repo", NamespaceID: "namespace", ChunkID: chunk, Vector: []byte{1, 2, 3, 4}, Dimensions: 1, DType: "float32"}); err != nil {
			t.Fatal(err)
		}
		path, err := store.checkpointPath("owner/repo", "namespace", chunk)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	activePath := save("active")
	orphanPath := save("orphan")
	oldPath := save("old")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}
	active := map[string]struct{}{VectorCheckpointIdentity("owner/repo", "namespace", "active"): {}, VectorCheckpointIdentity("owner/repo", "namespace", "old"): {}}
	result, err := store.Prune(ctx, VectorCheckpointRetentionPolicy{MaxAge: 24 * time.Hour, ActiveIdentities: active})
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesDeleted != 2 {
		t.Fatalf("deleted files = %d, want orphan and expired checkpoints", result.FilesDeleted)
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("active checkpoint was pruned: %v", err)
	}
	for _, path := range []string{orphanPath, oldPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale checkpoint %s remains: %v", filepath.Base(path), err)
		}
	}
	info, err := os.Stat(activePath)
	if err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-time.Hour)
	if err := os.Chtimes(activePath, older, older); err != nil {
		t.Fatal(err)
	}
	newerPath := save("newer")
	result, err = store.Prune(ctx, VectorCheckpointRetentionPolicy{MaxBytes: info.Size()})
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesDeleted != 1 || result.BytesRetained > info.Size() {
		t.Fatalf("byte prune result = %#v", result)
	}
	if _, err := os.Stat(newerPath); err != nil {
		t.Fatalf("newest checkpoint was not retained: %v", err)
	}
}
