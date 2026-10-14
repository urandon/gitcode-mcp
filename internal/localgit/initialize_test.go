package localgit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInitializeNewStoreAndRefusesExistingAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.git")
	store, err := Initialize(context.Background(), path, "fixture-project", "refs/heads/main")
	if err != nil {
		t.Fatal("initial Git store unavailable")
	}
	snap, err := store.Snapshot(context.Background(), "refs/heads/main")
	if err != nil || snap.Project().ID != "fixture-project" || snap.Project().NextIssue != 1 {
		t.Fatal("initial project metadata invalid")
	}
	if _, err := Initialize(context.Background(), path, "other", "refs/heads/main"); err == nil {
		t.Fatal("existing store overwritten")
	}
	after, err := store.Snapshot(context.Background(), "refs/heads/main")
	if err != nil || after.Commit() != snap.Commit() {
		t.Fatal("existing authority changed")
	}
	for _, ref := range []string{"HEAD", "refs/heads/a..b"} {
		invalid := filepath.Join(t.TempDir(), "invalid.git")
		if _, err := Initialize(context.Background(), invalid, "fixture", ref); err == nil {
			t.Fatal("invalid init ref accepted")
		}
		if _, err := os.Stat(invalid); !os.IsNotExist(err) {
			t.Fatal("failed init retained incomplete authority")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	missing := filepath.Join(t.TempDir(), "cancelled.git")
	if _, err := Initialize(ctx, missing, "fixture", "refs/heads/main"); !errors.Is(err, context.Canceled) {
		t.Fatal("init cancellation lost")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("pre-cancelled init created a store")
	}
}
