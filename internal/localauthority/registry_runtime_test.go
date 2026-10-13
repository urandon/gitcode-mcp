package localauthority

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func preparedFixture(t *testing.T) (*PreparedRegistry, *Registry, string) {
	t.Helper()
	s, r, dir := testStore(t)
	if s.Close() != nil {
		t.Fatal("fixture quiesce failed")
	}
	p, err := r.PrepareRuntime()
	if err != nil {
		t.Fatal("fixture registry preparation failed")
	}
	t.Cleanup(func() {
		if p.Close() != nil {
			t.Error("prepared capability leaked an active writer")
		}
	})
	return p, r, dir
}

func TestPreparedRegistryExistingAdmissionLifetimeAndRestart(t *testing.T) {
	p, r, dir := preparedFixture(t)
	t.Setenv("HOME", privateTestDirectory(t))
	t.Setenv("USER", "incorrect-account")
	s, err := p.OpenWriter(context.Background(), dir, testProject)
	if err != nil {
		t.Fatalf("prepared existing authority refused: %v", err)
	}
	first := createIssue(t, s, "synthetic-prepared-key")
	ro, err := OpenReadOnly(context.Background(), dir, testProject)
	if err != nil {
		t.Fatal("runtime guard conflicts with canonical SQLite reads")
	}
	_ = ro.Close()
	if err = p.Close(); err == nil {
		t.Fatal("capability closed under its live writer")
	}
	if other, err := p.OpenWriter(context.Background(), dir, testProject); err == nil {
		_ = other.Close()
		t.Fatal("second prepared writer admitted")
	}
	runChild(t, r, dir, "writer_conflict", 78)
	runPreparedChild(t, r, dir, "conflict", 78)
	// A copied registry namespace cannot bypass the independent physical lease.
	copyRoot := privateTestDirectory(t)
	body, err := os.ReadFile(filepath.Join(r.root, string(testProject)+".owner"))
	if err != nil || os.WriteFile(filepath.Join(copyRoot, string(testProject)+".owner"), body, 0600) != nil {
		t.Fatal("copied registry fixture failed")
	}
	copyRegistry, err := newRegistry(copyRoot)
	if err != nil {
		t.Fatal("copied registry fixture not qualified")
	}
	runPreparedChild(t, copyRegistry, dir, "conflict", 78)
	if s.Close() != nil || s.Close() != nil {
		t.Fatal("writer lifetime release failed")
	}
	next, err := p.OpenWriter(context.Background(), dir, testProject)
	if err != nil {
		t.Fatal("prepared authority restart failed")
	}
	replay := createIssue(t, next, "synthetic-prepared-key")
	if !replay.Replayed || replay.Citation != first.Citation {
		t.Fatal("restart changed confirmed identity")
	}
	if next.Close() != nil || p.Close() != nil || p.Close() != nil {
		t.Fatal("ordered lifetime close failed")
	}
	if s, err := p.OpenWriter(context.Background(), dir, testProject); err == nil {
		_ = s.Close()
		t.Fatal("closed preparation reused")
	}
}

func TestPreparedRegistryProcessHelper(t *testing.T) {
	if os.Getenv("GCM_PREPARED_CHILD") != "1" {
		return
	}
	r, err := newRegistry(os.Getenv("GCM_PREPARED_REGISTRY"))
	if err != nil {
		os.Exit(11)
	}
	p, err := r.PrepareRuntime()
	if err != nil {
		os.Exit(12)
	}
	s, err := p.OpenWriter(context.Background(), os.Getenv("GCM_PREPARED_STORE"), testProject)
	if os.Getenv("GCM_PREPARED_PHASE") == "conflict" {
		var e authority.Error
		if errors.As(err, &e) && e.Code == authority.Conflict {
			os.Exit(78)
		}
		os.Exit(13)
	}
	if err != nil {
		os.Exit(14)
	}
	createIssue(t, s, "synthetic-prepared-process-key")
	// Abrupt exit intentionally skips Store/PreparedRegistry.Close.
	os.Exit(77)
}

func runPreparedChild(t *testing.T, r *Registry, dir, phase string, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPreparedRegistryProcessHelper$")
	cmd.Env = []string{"GCM_PREPARED_CHILD=1", "GCM_PREPARED_REGISTRY=" + r.root, "GCM_PREPARED_STORE=" + dir, "GCM_PREPARED_PHASE=" + phase}
	var exited *exec.ExitError
	if !errors.As(cmd.Run(), &exited) || exited.ExitCode() != want {
		t.Fatal("prepared process did not reach its expected boundary")
	}
}

func TestPreparedRegistryAbruptExitReleasesBothKernelLeases(t *testing.T) {
	p, r, dir := preparedFixture(t)
	runPreparedChild(t, r, dir, "abrupt", 77)
	s, err := p.OpenWriter(context.Background(), dir, testProject)
	if err != nil {
		t.Fatal("runtime leases survived process exit")
	}
	defer s.Close()
	replay := createIssue(t, s, "synthetic-prepared-process-key")
	if !replay.Replayed || replay.ProjectSequence != 1 {
		t.Fatal("runtime restart lost or duplicated confirmed mutation")
	}
}

func TestPreparedRegistryPhysicalReplacementFencesBeforeMutation(t *testing.T) {
	for _, replace := range []string{"directory", "database"} {
		t.Run(replace, func(t *testing.T) {
			p, _, dir := preparedFixture(t)
			s, err := p.OpenWriter(context.Background(), dir, testProject)
			if err != nil {
				t.Fatal("fixture runtime admission failed")
			}
			defer s.Close()
			createIssue(t, s, "synthetic-before-physical-replacement")
			if replace == "directory" {
				if os.Rename(dir, dir+".retained") != nil || os.Mkdir(dir, 0700) != nil {
					t.Fatal("directory replacement fixture failed")
				}
				// Keep the exact database inode: database-only guards must not
				// accept a replacement authority directory around the old bytes.
				if os.Rename(filepath.Join(dir+".retained", "tracker.db"), filepath.Join(dir, "tracker.db")) != nil {
					t.Fatal("same-inode relocation fixture failed")
				}
			} else {
				path := filepath.Join(dir, "tracker.db")
				body, err := os.ReadFile(path)
				if err != nil || os.Rename(path, path+".retained") != nil || os.WriteFile(path, body, 0600) != nil {
					t.Fatal("database replacement fixture failed")
				}
			}
			if _, err = s.Mutate(context.Background(), request(t, "issue.create", "synthetic-after-physical-replacement", authority.IssuePayload{Title: "Fixture", State: "open"}), testActor); err == nil || !s.fenced {
				t.Fatal("physical replacement bypassed runtime admission fence")
			}
		})
	}
}

func TestPreparedRegistryNeverInitializesOwnerRecords(t *testing.T) {
	for _, state := range []string{"missing", "empty", "partial"} {
		t.Run(state, func(t *testing.T) {
			p, r, dir := preparedFixture(t)
			owner := filepath.Join(r.root, string(testProject)+".owner")
			body, err := os.ReadFile(owner)
			if err != nil {
				t.Fatal("fixture owner read failed")
			}
			if state == "missing" {
				if os.Rename(owner, owner+".retained") != nil {
					t.Fatal("fixture owner removal failed")
				}
			} else {
				if state == "empty" {
					body = nil
				} else {
					body = body[:len(body)-65]
				}
				if os.WriteFile(owner, body, 0600) != nil {
					t.Fatal("fixture owner edit failed")
				}
			}
			_, err = p.OpenWriter(context.Background(), dir, testProject)
			if err == nil {
				t.Fatal("unprepared binding gained writer authority")
			}
			if strings.Contains(err.Error(), r.root) || strings.Contains(err.Error(), dir) {
				t.Fatal("admission error leaked private coordinates")
			}
			got, readErr := os.ReadFile(owner)
			if state == "missing" {
				if !os.IsNotExist(readErr) {
					t.Fatal("runtime created missing owner record")
				}
			} else if readErr != nil || string(got) != string(body) {
				t.Fatal("runtime upgraded incomplete binding")
			}
		})
	}
}

func TestPreparedRegistryRefusesEmptyCapabilitiesAndDoesNotCreateStores(t *testing.T) {
	dir := privateTestDirectory(t)
	for _, p := range []*PreparedRegistry{nil, {}} {
		if s, err := p.OpenWriter(context.Background(), dir, testProject); err == nil {
			_ = s.Close()
			t.Fatal("unprepared capability admitted")
		}
	}
	p, _, _ := preparedFixture(t)
	if s, err := p.OpenWriter(context.Background(), dir, testProject); err == nil {
		_ = s.Close()
		t.Fatal("empty store implicitly initialized")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("refused runtime admission created store files")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.OpenWriter(ctx, dir, testProject); !errors.Is(err, context.Canceled) {
		t.Fatal("admission ignored cancellation")
	}
}

func TestPreparedRegistryRootReplacementFencesBeforeMutation(t *testing.T) {
	p, r, dir := preparedFixture(t)
	s, err := p.OpenWriter(context.Background(), dir, testProject)
	if err != nil {
		t.Fatal("fixture runtime admission failed")
	}
	defer s.Close()
	createIssue(t, s, "synthetic-before-registry-replacement")
	if os.Rename(r.root, r.root+".retained") != nil || os.Mkdir(r.root, 0700) != nil {
		t.Fatal("registry replacement fixture failed")
	}
	if _, err = s.Mutate(context.Background(), request(t, "issue.create", "synthetic-after-registry-replacement", authority.IssuePayload{Title: "Fixture", State: "open"}), testActor); err == nil {
		t.Fatal("writer acknowledged after registry identity changed")
	}
	status, err := s.Status(context.Background())
	if err != nil || status.AuthorityGeneration != 1 || !s.fenced {
		t.Fatal("registry replacement changed authority or did not fence admission")
	}
	if _, err = p.OpenWriter(context.Background(), dir, testProject); err == nil {
		t.Fatal("stale retained registry accepted new writer")
	}
}

func TestPreparedRegistryPhysicalGuardAndCloneRefusal(t *testing.T) {
	p, r, dir := preparedFixture(t)
	s, err := p.OpenWriter(context.Background(), dir, testProject)
	if err != nil {
		t.Fatal("fixture runtime admission failed")
	}
	guard, err := openPrivateDirectory(dir)
	if err != nil {
		t.Fatal("fixture physical guard unavailable")
	}
	if lockExclusive(guard) == nil {
		_ = guard.Close()
		_ = s.Close()
		t.Fatal("runtime did not hold physical authority directory guard")
	}
	_ = guard.Close()
	if s.Close() != nil {
		t.Fatal("fixture quiesce failed")
	}
	clone := privateTestDirectory(t)
	body, err := os.ReadFile(filepath.Join(dir, "tracker.db"))
	if err != nil || os.WriteFile(filepath.Join(clone, "tracker.db"), body, 0600) != nil {
		t.Fatal("clone fixture failed")
	}
	if other, err := p.OpenWriter(context.Background(), clone, testProject); err == nil {
		_ = other.Close()
		t.Fatal("clone bypassed global identity binding")
	}
	next, err := OpenWriter(context.Background(), r, dir, testProject)
	if err != nil {
		t.Fatal("runtime physical guard not released")
	}
	_ = next.Close()
}

func TestPreparedRegistryConcurrentAdmissionAndClose(t *testing.T) {
	p, _, dir := preparedFixture(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := p.OpenWriter(context.Background(), dir, testProject)
			if err == nil && s.Close() != nil {
				t.Error("concurrent writer release failed")
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); <-start; _ = p.Close() }()
	close(start)
	wg.Wait()
	if p.Close() != nil {
		t.Fatal("joined runtime could not release preparation")
	}
}

func TestPreparedRegistryContendedAdmissionDoesNotWaitForOtherWork(t *testing.T) {
	p, _, dir := preparedFixture(t)
	p.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.OpenWriter(ctx, dir, testProject); done <- err }()
	var err error
	select {
	case err = <-done:
		p.mu.Unlock()
	case <-time.After(time.Second):
		p.mu.Unlock()
		<-done // Join an implementation that incorrectly waited on the mutex.
		t.Fatal("contended admission waited for unrelated work past its deadline")
	}
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != authority.Conflict {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("contended admission lost its typed refusal")
		}
	}
	p.mu.Lock()
	cancel()
	_, err = p.OpenWriter(ctx, dir, testProject)
	p.mu.Unlock()
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancelled contended admission lost cancellation identity")
	}
}
