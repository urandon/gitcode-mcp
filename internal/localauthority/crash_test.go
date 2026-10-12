package localauthority

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func TestAuthorityProcessHelper(t *testing.T) {
	if os.Getenv("GCM_AUTHORITY_TEST_CHILD") != "1" {
		return
	}
	r, err := newRegistry(os.Getenv("GCM_AUTHORITY_TEST_REGISTRY"))
	if err != nil {
		os.Exit(11)
	}
	if os.Getenv("GCM_AUTHORITY_TEST_INIT") == "1" {
		s, err := initialize(context.Background(), r, os.Getenv("GCM_AUTHORITY_TEST_DIRECTORY"), testManifest(), func(at string) error {
			if at == os.Getenv("GCM_AUTHORITY_TEST_PHASE") {
				os.Exit(77)
			}
			return nil
		})
		if err != nil {
			os.Exit(16)
		}
		_ = s.Close()
		os.Exit(15)
	}
	s, err := OpenWriter(context.Background(), r, os.Getenv("GCM_AUTHORITY_TEST_DIRECTORY"), testProject)
	if os.Getenv("GCM_AUTHORITY_TEST_PHASE") == "writer_conflict" {
		var typed authority.Error
		if errors.As(err, &typed) && typed.Code == authority.Conflict {
			os.Exit(78)
		}
		os.Exit(12)
	}
	if err != nil {
		os.Exit(13)
	}
	s.fault = func(at string) error {
		if at == os.Getenv("GCM_AUTHORITY_TEST_PHASE") {
			os.Exit(77)
		}
		return nil
	}
	_, err = s.Mutate(context.Background(), request(t, "issue.create", "synthetic-process-key", authority.IssuePayload{Title: "Synthetic process task", Body: "synthetic evidence", State: "open"}), testActor)
	if err != nil {
		os.Exit(14)
	}
	os.Exit(15)
}

func runChild(t *testing.T, r *Registry, dir, phase string, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuthorityProcessHelper$")
	cmd.Env = append(os.Environ(), "GCM_AUTHORITY_TEST_CHILD=1", "GCM_AUTHORITY_TEST_REGISTRY="+r.root, "GCM_AUTHORITY_TEST_DIRECTORY="+dir, "GCM_AUTHORITY_TEST_PHASE="+phase)
	// Child diagnostics are deliberately discarded, not copied into evidence.
	err := cmd.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != want {
		t.Fatal("isolated authority process did not reach expected boundary")
	}
}

func TestKernelProjectLeaseRefusesAnotherProcess(t *testing.T) {
	s, r, dir := testStore(t)
	createIssue(t, s, "synthetic-process-parent")
	runChild(t, r, dir, "writer_conflict", 78)
	if s.lease == nil {
		t.Fatal("foreign process revoked writer authority")
	}
}

func TestAbruptExitRecoveryBeforeAndAfterEveryCommitBarrier(t *testing.T) {
	for _, phase := range []string{"revision", "relations", "receipt", "generation", "database_sync", "wal_sync", "directory_sync", "acknowledgement"} {
		t.Run(phase, func(t *testing.T) {
			s, r, dir := testStore(t)
			if s.Close() != nil {
				t.Fatal("quiesce failed")
			}
			runChild(t, r, dir, phase, 77)
			recovered, err := OpenWriter(context.Background(), r, dir, testProject)
			if err != nil {
				t.Fatalf("authority recovery failed: %T", err)
			}
			defer recovered.Close()
			status, err := recovered.Status(context.Background())
			if err != nil {
				t.Fatal("recovered status unavailable")
			}
			committed := phase == "database_sync" || phase == "wal_sync" || phase == "directory_sync" || phase == "acknowledgement"
			want := uint64(0)
			if committed {
				want = 1
			}
			if status.AuthorityGeneration != want {
				t.Fatal("partial tuple survived process crash")
			}
			m := request(t, "issue.create", "synthetic-process-key", authority.IssuePayload{Title: "Synthetic process task", Body: "synthetic evidence", State: "open"})
			receipt, err := recovered.Mutate(context.Background(), m, testActor)
			if err != nil || receipt.ProjectSequence != 1 || receipt.Replayed != committed {
				t.Fatal("recovery replay duplicated or lost operation")
			}
			for _, table := range []string{"entities", "revisions", "receipts", "audit"} {
				var count int
				if recovered.db.QueryRow("SELECT count(*) FROM "+table).Scan(&count) != nil || count != 1 {
					t.Fatal("recovery tuple is not atomic")
				}
			}
		})
	}
}

func TestPublicationFailureFencesNewWritesButSameKeyCanConfirm(t *testing.T) {
	for _, phase := range []string{"database_sync", "wal_sync", "directory_sync"} {
		t.Run(phase, func(t *testing.T) {
			s, _, _ := testStore(t)
			m := request(t, "issue.create", "synthetic-publication", authority.IssuePayload{Title: "Fixture", Body: "", State: "open"})
			s.fault = func(at string) error {
				if at == phase {
					return Error{DurabilityUnavailable}
				}
				return nil
			}
			if _, err := s.Mutate(context.Background(), m, testActor); err == nil {
				t.Fatal("unqualified publication acknowledged")
			}
			status, err := s.Status(context.Background())
			if err != nil || status.Readiness != "recovery_required" || status.WriterState != "quiesced" {
				t.Fatal("uncertain acknowledgement did not fence new admission")
			}
			other := m
			other.Intent.IdempotencyKey = "synthetic-new-key"
			if _, err := s.Mutate(context.Background(), other, testActor); err == nil {
				t.Fatal("new key bypassed publication fence")
			}
			s.fault = nil
			r, err := s.Mutate(context.Background(), m, testActor)
			if err != nil || !r.Replayed || r.ProjectSequence != 1 {
				t.Fatal("same-key durability confirmation duplicated operation")
			}
		})
	}
}
