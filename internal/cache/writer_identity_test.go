package cache

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
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
