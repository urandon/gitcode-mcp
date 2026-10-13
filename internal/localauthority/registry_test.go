package localauthority

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestRegistryPathABAKeepsLeaseOnQualifiedDirectory(t *testing.T) {
	r, _ := initializationFixture(t)
	if r.checkCapability() != nil {
		t.Fatal("prepared registry failed admission")
	}
	original := r.root + ".original"
	replacement := r.root + ".replacement"
	t.Cleanup(func() { _ = os.RemoveAll(original); _ = os.RemoveAll(replacement) })
	if os.Rename(r.root, original) != nil || os.Mkdir(r.root, 0700) != nil {
		t.Fatal("registry ABA fixture unavailable")
	}
	l, err := r.lockProject(context.Background(), testProject)
	if err != nil {
		t.Fatal("retained registry lease could not open")
	}
	defer l.close()
	if _, err := os.Lstat(filepath.Join(original, string(testProject)+".owner")); err != nil {
		t.Fatal("lease did not remain on qualified registry")
	}
	if entries, err := os.ReadDir(r.root); err != nil || len(entries) != 0 {
		t.Fatal("pathname replacement received ownership effects")
	}
	if os.Rename(r.root, replacement) != nil || os.Rename(original, r.root) != nil {
		t.Fatal("registry ABA restoration unavailable")
	}
	if r.checkCapability() != nil {
		t.Fatal("restored qualified registry failed admission")
	}
	other, err := newRegistry(r.root)
	if err != nil {
		t.Fatal("second prepared capability unavailable")
	}
	defer other.Close()
	second, err := other.lockProject(context.Background(), testProject)
	if err == nil {
		_ = second.close()
		t.Fatal("ABA split machine-global UUID ownership")
	}
	var code authority.Error
	if !errors.As(err, &code) || code.Code != authority.Conflict {
		t.Fatal("retained lease did not produce typed contention")
	}
}

func TestRegistryInitializationStaysOnRetainedDirectory(t *testing.T) {
	r, directory := initializationFixture(t)
	original := r.root + ".original"
	replacement := r.root + ".replacement"
	t.Cleanup(func() { _ = os.RemoveAll(original); _ = os.RemoveAll(replacement) })
	s, err := initialize(context.Background(), r, directory, testManifest(), func(phase string) error {
		if phase == "init_lease" {
			if os.Rename(r.root, original) != nil || os.Mkdir(r.root, 0700) != nil {
				t.Fatal("initialization ABA fixture unavailable")
			}
		}
		if phase == "init_journal_linked" || phase == "init_journal_published" || phase == "init_binding_published" || phase == "init_pending_cleared" {
			if entries, err := os.ReadDir(r.root); err != nil || len(entries) != 0 {
				t.Fatal("replacement received initialization effects")
			}
		}
		if phase == "init_acknowledgement" {
			if os.Rename(r.root, replacement) != nil || os.Rename(original, r.root) != nil {
				t.Fatal("initialization ABA restore unavailable")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal("retained initialization failed")
	}
	defer s.Close()
	if _, pending, err := readPending(r, testProject); err != nil || pending {
		t.Fatal("qualified initialization did not clear its own journal")
	}
	if entries, err := os.ReadDir(replacement); err != nil || len(entries) != 0 {
		t.Fatal("initialization wrote to replacement registry")
	}
}

func TestRegistryCloseDoesNotReleaseActiveLease(t *testing.T) {
	s, r, directory := testStore(t)
	if r.Close() != nil || r.Close() != nil {
		t.Fatal("registry capability close failed")
	}
	if syncRegistryDirectory(s.lease.registry) != nil {
		t.Fatal("capability close invalidated lease directory")
	}
	if l, err := r.lockProject(context.Background(), testProject); err == nil {
		_ = l.close()
		t.Fatal("closed capability admitted new owner")
	}
	other, err := newRegistry(r.root)
	if err != nil {
		t.Fatal("independent registry capability unavailable")
	}
	defer other.Close()
	if writer, err := OpenWriter(context.Background(), other, directory, testProject); err == nil {
		_ = writer.Close()
		t.Fatal("capability close released active writer")
	}
	if s.Close() != nil {
		t.Fatal("retained writer close failed")
	}
	writer, err := OpenWriter(context.Background(), other, directory, testProject)
	if err != nil {
		t.Fatal("closed writer retained lease")
	}
	_ = writer.Close()
}
