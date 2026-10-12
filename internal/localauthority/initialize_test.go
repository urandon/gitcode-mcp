package localauthority

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func initializationFixture(t *testing.T) (*Registry, string) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("writer persistence is not qualified on this platform")
	}
	r, err := newRegistry(privateTestDirectory(t))
	if err != nil {
		t.Fatal("isolated initialization registry unavailable")
	}
	return r, privateTestDirectory(t)
}

func TestInitializationForeignConflictDoesNotReserveIdentity(t *testing.T) {
	r, dir := initializationFixture(t)
	final := filepath.Join(dir, "tracker.db")
	const foreign = "synthetic foreign file"
	if os.WriteFile(final, []byte(foreign), 0600) != nil {
		t.Fatal("foreign fixture unavailable")
	}
	if s, err := Initialize(context.Background(), r, dir, testManifest()); err == nil {
		_ = s.Close()
		t.Fatal("foreign file promoted")
	}
	body, err := os.ReadFile(final)
	if err != nil || string(body) != foreign {
		t.Fatal("foreign file overwritten")
	}
	// The fixture owner safely moves its own unrelated file; initialization
	// itself must never do so and must not poison the later legitimate retry.
	if os.Rename(final, filepath.Join(dir, "synthetic-retained-foreign")) != nil {
		t.Fatal("fixture owner move failed")
	}
	s, err := Initialize(context.Background(), r, dir, testManifest())
	if err != nil {
		t.Fatal("benign initialization conflict poisoned project identity")
	}
	defer s.Close()
	status, err := s.Status(context.Background())
	if err != nil || status.AuthorityGeneration != 0 {
		t.Fatal("initial authority unexpectedly contains mutations")
	}
}

func runInitializationChild(t *testing.T, r *Registry, dir, phase string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuthorityProcessHelper$")
	cmd.Env = append(os.Environ(), "GCM_AUTHORITY_TEST_CHILD=1", "GCM_AUTHORITY_TEST_INIT=1", "GCM_AUTHORITY_TEST_REGISTRY="+r.root, "GCM_AUTHORITY_TEST_DIRECTORY="+dir, "GCM_AUTHORITY_TEST_PHASE="+phase)
	err := cmd.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 77 {
		t.Fatal("initialization subprocess did not reach expected boundary")
	}
}

func TestInitializationAbruptExitPublicationRetry(t *testing.T) {
	for _, phase := range []string{"init_lease", "init_stage_directory", "init_file", "init_schema", "init_commit", "init_checkpoint", "database_sync", "wal_sync", "directory_sync", "init_stage_closed", "init_journal_prepared", "init_journal_linked", "init_journal_published", "init_final_linked", "init_final_preserved", "init_binding_written", "init_binding_published", "init_pending_cleared", "init_acknowledgement"} {
		t.Run(phase, func(t *testing.T) {
			r, dir := initializationFixture(t)
			runInitializationChild(t, r, dir, phase)
			p, pending, err := readPending(r, testProject)
			if err != nil {
				t.Fatal("known pending initialization is not recoverable")
			}
			if pending {
				_, err = OpenWriter(context.Background(), r, dir, testProject)
				var typed Error
				if !errors.As(err, &typed) || typed.Code != InitializationPending {
					t.Fatal("ordinary writer bypassed initialization fence")
				}
				changed := testManifest()
				changed.Slug = "different-synthetic-intent"
				if s, err := Initialize(context.Background(), r, dir, changed); err == nil {
					_ = s.Close()
					t.Fatal("changed initialization intent resumed")
				}
			}
			s, err := Initialize(context.Background(), r, dir, testManifest())
			if err != nil {
				t.Fatalf("same-intent initialization retry failed: %T", err)
			}
			if pending && s.instance != p.Instance {
				_ = s.Close()
				t.Fatal("published pending identity changed on retry")
			}
			instance := s.instance
			status, err := s.Status(context.Background())
			if err != nil || status.AuthorityGeneration != 0 {
				_ = s.Close()
				t.Fatal("empty initialization replay created a mutation")
			}
			if s.Close() != nil {
				t.Fatal("initialized authority did not close")
			}
			if _, pending, err := readPending(r, testProject); err != nil || pending {
				t.Fatal("confirmed initialization retained pending fence")
			}
			// A lost final acknowledgement also reuses the existing final binding,
			// never a new instance or a second database.
			again, err := Initialize(context.Background(), r, dir, testManifest())
			if err != nil {
				t.Fatal("confirmed initialization replay failed")
			}
			if again.instance != instance {
				_ = again.Close()
				t.Fatal("initialization replay allocated another instance")
			}
			_ = again.Close()
		})
	}
}

func pendingInitializationFixture(t *testing.T) (*Registry, string, pendingInitialization) {
	t.Helper()
	r, dir := initializationFixture(t)
	_, err := initialize(context.Background(), r, dir, testManifest(), func(phase string) error {
		if phase == "init_journal_published" {
			return Error{DurabilityUnavailable}
		}
		return nil
	})
	if err == nil {
		t.Fatal("pending publication fixture did not interrupt")
	}
	p, pending, err := readPending(r, testProject)
	if err != nil || !pending {
		t.Fatal("pending publication fixture unavailable")
	}
	return r, dir, p
}

func TestPendingInitializationNeverOverwritesForeignFinal(t *testing.T) {
	r, dir, _ := pendingInitializationFixture(t)
	final := filepath.Join(dir, "tracker.db")
	const foreign = "synthetic foreign destination"
	if os.WriteFile(final, []byte(foreign), 0600) != nil {
		t.Fatal("foreign fixture unavailable")
	}
	if s, err := Initialize(context.Background(), r, dir, testManifest()); err == nil {
		_ = s.Close()
		t.Fatal("pending intent overwrote an unknown final file")
	}
	body, err := os.ReadFile(final)
	if err != nil || string(body) != foreign {
		t.Fatal("unknown final bytes changed")
	}
	if os.Rename(final, filepath.Join(dir, "synthetic-retained-foreign")) != nil {
		t.Fatal("fixture owner move failed")
	}
	s, err := Initialize(context.Background(), r, dir, testManifest())
	if err != nil {
		t.Fatal("foreign destination conflict poisoned pending retry")
	}
	_ = s.Close()
}

func TestPendingInitializationRefusesPhysicalReplacement(t *testing.T) {
	r, dir, p := pendingInitializationFixture(t)
	stage := filepath.Join(dir, stageName(p.Instance), "tracker.db")
	body, err := os.ReadFile(stage)
	if err != nil || os.Rename(stage, stage+".retained") != nil || os.WriteFile(stage, body, 0600) != nil {
		t.Fatal("physical replacement fixture unavailable")
	}
	if s, err := Initialize(context.Background(), r, dir, testManifest()); err == nil {
		_ = s.Close()
		t.Fatal("cloned staging identity was promoted")
	}
	if _, err = os.Lstat(filepath.Join(dir, "tracker.db")); !os.IsNotExist(err) {
		t.Fatal("unverified staging published final name")
	}
}

func TestPendingInitializationRefusesDirectoryReplacement(t *testing.T) {
	r, dir, p := pendingInitializationFixture(t)
	stageDir := filepath.Join(dir, stageName(p.Instance))
	if os.Rename(stageDir, stageDir+".retained") != nil || os.Mkdir(stageDir, 0700) != nil {
		t.Fatal("directory replacement fixture unavailable")
	}
	if s, err := Initialize(context.Background(), r, dir, testManifest()); err == nil {
		_ = s.Close()
		t.Fatal("unverified staging directory was accepted")
	}
	if _, err := os.Lstat(stageDir); err != nil {
		t.Fatal("unknown replacement directory was removed")
	}
}

func TestPendingInitializationIsGloballyProjectScoped(t *testing.T) {
	r, dir, p := pendingInitializationFixture(t)
	other := privateTestDirectory(t)
	if s, err := Initialize(context.Background(), r, other, testManifest()); err == nil {
		_ = s.Close()
		t.Fatal("another profile replaced pending project ownership")
	}
	s, err := Initialize(context.Background(), r, dir, testManifest())
	if err != nil {
		t.Fatal("foreign profile conflict poisoned original pending intent")
	}
	if s.instance != p.Instance {
		_ = s.Close()
		t.Fatal("global pending identity changed")
	}
	_ = s.Close()
}

func TestInterruptedStagingAllocationIsBounded(t *testing.T) {
	r, dir := initializationFixture(t)
	for i := 0; i < 32; i++ {
		_, err := initialize(context.Background(), r, dir, testManifest(), func(phase string) error {
			if phase == "init_stage_directory" {
				return Error{DurabilityUnavailable}
			}
			return nil
		})
		if err == nil {
			t.Fatal("staging fault was not applied")
		}
	}
	_, err := Initialize(context.Background(), r, dir, testManifest())
	var typed Error
	if !errors.As(err, &typed) || typed.Code != Capacity {
		t.Fatal("repeated interrupted initialization exceeded staging bound")
	}
}
