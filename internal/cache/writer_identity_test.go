package cache

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateMemoryWriterIdentity(t *testing.T) {
	ctx := context.Background()
	a, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, store := range []*SQLiteStore{a, b} {
		name := filepath.Base(store.lockPath)
		identity := strings.TrimSuffix(strings.TrimPrefix(name, "gitcode-mcp-memory-writer-"), ".lock")
		decoded, err := hex.DecodeString(identity)
		if err != nil || len(decoded) != 16 {
			t.Fatal("private memory writer must use a 128-bit database identity, not a pointer name")
		}
	}
	if a.lockPath == b.lockPath {
		t.Fatal("independent private databases shared a writer identity")
	}
	aLease, err := a.AcquireWriter(ctx, WriterRequest{Operation: "identity-holder"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.ReleaseWriter(ctx, aLease)
	bLease, err := b.AcquireWriter(ctx, WriterRequest{Operation: "independent-holder"})
	if err != nil {
		t.Fatalf("independent writer error type=%T", err)
	}
	if err := b.ReleaseWriter(ctx, bLease); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.lockPath); !os.IsNotExist(err) {
		t.Fatal("closed private store retained its ephemeral lock")
	}
	if _, err := os.Stat(a.lockPath); err != nil {
		t.Fatal("closing an unrelated store removed the active writer lock")
	}
}

func TestPhysicalCacheWriterAuthorityIncludesMigration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("filesystem symlink creation requires privileges on Windows")
	}
	ctx := context.Background()
	root := t.TempDir()
	realPath := filepath.Join(root, "cache.db")
	aliasPath := filepath.Join(root, "alias.db")
	store, err := NewSQLiteStore(ctx, realPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := os.Symlink(realPath, aliasPath); err != nil {
		t.Fatal("file symlink creation failed")
	}
	alias, err := NewSQLiteStore(ctx, aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	defer alias.Close()
	if store.lockPath != alias.lockPath {
		t.Fatal("physical cache aliases have different default writer authorities")
	}
	lease, err := store.AcquireWriter(ctx, WriterRequest{Operation: "physical-holder"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseWriter(ctx, lease)
	_, err = MigrateCacheWithConfirm(ctx, aliasPath, false, Confirmation{Confirmed: true})
	var busy ErrLockContention
	if !errors.As(err, &busy) {
		t.Fatalf("migration bypassed physical writer authority; error type=%T", err)
	}
	if err := store.ReleaseWriter(ctx, lease); err != nil {
		t.Fatal(err)
	}
	result, err := MigrateCacheWithConfirm(ctx, aliasPath, false, Confirmation{Confirmed: true})
	if err != nil || result.FromVersion != currentSchemaVersion || result.ToVersion != currentSchemaVersion {
		t.Fatal("migration did not resume normally after release")
	}
}

func TestCanonicalCacheOpenSurvivesAliasRetarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("filesystem symlink creation requires privileges on Windows")
	}
	ctx := context.Background()
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.db")
	secondPath := filepath.Join(root, "second.db")
	aliasPath := filepath.Join(root, "alias.db")
	first, err := NewSQLiteStore(ctx, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewSQLiteStore(ctx, secondPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	want, err := first.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := second.CacheIdentity(ctx)
	if err != nil || want.UUID == other.UUID {
		t.Fatal("independent fixtures did not have distinct cache identities")
	}
	if err := os.Symlink(firstPath, aliasPath); err != nil {
		t.Fatal("fixture symlink creation failed")
	}
	aliased, err := NewSQLiteStore(ctx, aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	defer aliased.Close()
	if err := os.Remove(aliasPath); err != nil {
		t.Fatal("fixture symlink removal failed")
	}
	if err := os.Symlink(secondPath, aliasPath); err != nil {
		t.Fatal("fixture symlink retarget failed")
	}
	// Force a new pool connection. It must reopen the canonical file whose
	// authority was selected, not the mutable alias now pointing elsewhere.
	aliased.db.SetMaxIdleConns(0)
	got, err := aliased.CacheIdentity(ctx)
	if err != nil || got.UUID != want.UUID || aliased.lockPath != first.lockPath {
		t.Fatal("replacement connection escaped the canonical writer authority")
	}
}

func TestWriterAuthorityResolutionErrorsAreOpaque(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("filesystem symlink creation requires privileges on Windows")
	}
	root := t.TempDir()
	targetParent := filepath.Join(root, "private-target-missing")
	alias := filepath.Join(root, "alias.db")
	if err := os.Symlink(filepath.Join(targetParent, "cache.db"), alias); err != nil {
		t.Fatal("fixture symlink creation failed")
	}
	for _, open := range []func() error{
		func() error { _, err := NewSQLiteStore(context.Background(), alias); return err },
		func() error {
			_, err := cachePathForDataSource(alias)
			return err
		},
	} {
		err := open()
		var authority ErrWriterAuthorityResolution
		var pathError *os.PathError
		if !errors.As(err, &authority) || !errors.Is(err, os.ErrNotExist) || errors.As(err, &pathError) {
			t.Fatal("authority error lost its safe classification or retained a pathname-bearing cause")
		}
		if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "private-target-missing") {
			t.Fatal("writer authority error disclosed a filesystem coordinate")
		}
	}
}
