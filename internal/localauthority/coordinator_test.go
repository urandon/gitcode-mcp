package localauthority

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func coordinatorFixture(t *testing.T) (*Coordinator, *PreparedRegistry, *Registry, string) {
	t.Helper()
	p, r, dir := preparedFixture(t)
	c, err := NewStrictCoordinator(context.Background(), p, []ProjectBinding{{testProject, dir}})
	if err != nil {
		t.Fatal("strict coordinator fixture admission failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if c.Close(ctx) != nil {
			t.Error("coordinator fixture did not join")
		}
	})
	return c, p, r, dir
}

func TestCoordinatorCanonicalRoundTripReplayAndRestart(t *testing.T) {
	c, p, _, dir := coordinatorFixture(t)
	ctx := context.Background()
	m := request(t, "issue.create", "synthetic-coordinator-key", authority.IssuePayload{Title: "Synthetic typed source", Body: "persistent evidence", State: "open"})
	first, err := c.Mutate(ctx, m, testActor)
	if err != nil || first.Validate() != nil {
		t.Fatal("coordinator canonical mutation failed")
	}
	replay, err := c.Mutate(ctx, m, testActor)
	if err != nil || !replay.Replayed || replay.Citation != first.Citation {
		t.Fatal("coordinator replay changed confirmed authority")
	}
	read, err := c.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: first.Citation.EntityID, Revision: 1, Operation: "get"})
	if err != nil || len(read.Entities) != 1 || !strings.Contains(string(read.Entities[0].Payload), "persistent evidence") {
		t.Fatal("canonical citation did not resolve")
	}
	search, err := c.LexicalSearch(ctx, LexicalRequest{ProjectID: testProject, Query: "persistent evidence", Limit: 8})
	if err != nil || len(search.Hits) != 1 || search.Hits[0].Citation != first.Citation {
		t.Fatal("coordinator did not perform real lexical retrieval")
	}
	unknown := authority.UUID("22222222-2222-4222-8222-222222222222")
	_, err = c.Status(ctx, unknown)
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != authority.NoAuthority {
		t.Fatal("unknown UUID fell back to another authority")
	}
	if c.Close(ctx) != nil {
		t.Fatal("coordinator close failed")
	}
	next, err := NewStrictCoordinator(ctx, p, []ProjectBinding{{testProject, dir}})
	if err != nil {
		t.Fatal("same prepared authority failed to restart")
	}
	defer next.Close(ctx)
	replay, err = next.Mutate(ctx, m, testActor)
	if err != nil || !replay.Replayed || replay.Citation != first.Citation {
		t.Fatal("coordinator restart lost confirmed identity")
	}
}

func TestCoordinatorIntakeRefusesMixedPoliciesBeforeWriterAdmission(t *testing.T) {
	p, r, dir := preparedFixture(t)
	ids := []authority.UUID{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}
	for index, policy := range []authority.NetworkPolicy{{DataEgress: "loopback_embeddings", ControlTransport: "ipc_only"}, {DataEgress: "deny", ControlTransport: "loopback_admin"}} {
		t.Run(policy.Label(), func(t *testing.T) {
			other := privateTestDirectory(t)
			m := testManifest()
			m.ID = ids[index]
			m.Network = policy
			s, err := Initialize(context.Background(), r, other, m)
			if err != nil || s.Close() != nil {
				t.Fatal("mixed policy fixture failed")
			}
			bindings := []ProjectBinding{{testProject, dir}, {m.ID, other}}
			c, err := NewStrictCoordinator(context.Background(), p, bindings)
			if c != nil {
				_ = c.Close(context.Background())
				t.Fatal("mixed cohort admitted")
			}
			var typed authority.Error
			if !errors.As(err, &typed) || typed.Code != authority.PolicyDenied {
				t.Fatal("mixed policy did not return typed refusal")
			}
			// Both original UUID leases remain free: no partial writer survives.
			for _, b := range bindings {
				writer, err := OpenWriter(context.Background(), r, b.Directory, b.ProjectID)
				if err != nil {
					t.Fatal("rejected cohort retained or changed ownership")
				}
				_ = writer.Close()
			}
		})
	}
}

func TestCoordinatorNeverReadsInheritedSetupOrCreatesAnEmptyStore(t *testing.T) {
	p, _, dir := preparedFixture(t)
	unused := privateTestDirectory(t)
	t.Setenv("HOME", unused)
	t.Setenv("GITCODE_MCP_CACHE_PATH", filepath.Join(unused, "unexpected-cache"))
	t.Setenv("GITCODE_MCP_CONFIG", filepath.Join(unused, "unexpected-config"))
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://synthetic.invalid:9")
	c, err := NewStrictCoordinator(context.Background(), p, []ProjectBinding{{testProject, dir}})
	if err != nil {
		t.Fatal("inherited unrelated settings changed strict admission")
	}
	if c.Close(context.Background()) != nil {
		t.Fatal("strict coordinator did not close")
	}
	if entries, err := os.ReadDir(unused); err != nil || len(entries) != 0 {
		t.Fatal("strict coordinator performed inherited setup")
	}
	_, err = NewStrictCoordinator(context.Background(), p, []ProjectBinding{{testProject, unused}})
	if err == nil {
		t.Fatal("empty authority implicitly initialized")
	}
	if entries, err := os.ReadDir(unused); err != nil || len(entries) != 0 {
		t.Fatal("cold refusal created authority/cache files")
	}
}

func TestCoordinatorContractBoundsAndUnpreparedRefusal(t *testing.T) {
	p, _, dir := preparedFixture(t)
	for _, bindings := range [][]ProjectBinding{nil, {{testProject, dir}, {testProject, dir}}, make([]ProjectBinding, CoordinatorProjectLimit+1), {{authority.UUID("invalid"), dir}}} {
		if c, err := NewStrictCoordinator(context.Background(), p, bindings); err == nil {
			_ = c.Close(context.Background())
			t.Fatal("invalid/beyond-budget intake admitted")
		}
	}
	for _, empty := range []*PreparedRegistry{nil, {}} {
		if c, err := NewStrictCoordinator(context.Background(), empty, []ProjectBinding{{testProject, dir}}); err == nil {
			_ = c.Close(context.Background())
			t.Fatal("missing preparation admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewStrictCoordinator(ctx, p, []ProjectBinding{{testProject, dir}}); !errors.Is(err, context.Canceled) {
		t.Fatal("bootstrap ignored cancellation")
	}
	for _, empty := range []*Coordinator{nil, {}} {
		if _, err := empty.Status(context.Background(), testProject); err == nil {
			t.Fatal("uninitialized coordinator became available")
		}
	}
}

func TestCoordinatorContendedProjectReturnsInsteadOfWaiting(t *testing.T) {
	c, _, _, _ := coordinatorFixture(t)
	c.mu.Lock()
	available := c.Available()
	c.mu.Unlock()
	if !available {
		t.Fatal("admission contention fabricated an unavailable runtime capability")
	}
	_, call, release, err := c.begin(context.Background(), testProject)
	if err != nil {
		t.Fatal("fixture admission unavailable")
	}
	defer release()
	if deadline, ok := call.Deadline(); !ok || time.Until(deadline) > CoordinatorCallBudget {
		t.Fatal("coordinator call has no bounded deadline")
	}
	done := make(chan error, 1)
	go func() { _, err := c.Status(context.Background(), testProject); done <- err }()
	select {
	case err := <-done:
		var typed authority.Error
		if !errors.As(err, &typed) || typed.Code != authority.Conflict {
			t.Fatal("contended project lost typed refusal")
		}
	case <-time.After(time.Second):
		release()
		<-done
		t.Fatal("contended project waited behind active work")
	}
}

func TestCoordinatorShutdownRetainsWriterUntilActualMutationJoins(t *testing.T) {
	c, p, _, dir := coordinatorFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	s := c.projects[testProject].store
	s.fault = func(at string) error {
		if at == "database_sync" {
			close(entered)
			<-release
		}
		return nil
	}
	m := request(t, "issue.create", "synthetic-shutdown-key", authority.IssuePayload{Title: "Fixture", State: "open"})
	done := make(chan error, 1)
	go func() { _, err := c.Mutate(context.Background(), m, testActor); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("actual mutation did not reach publication boundary")
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := c.Close(shutdown)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		<-done
		t.Fatal("shutdown claimed to join blocked mutation")
	}
	if p.Close() == nil {
		close(release)
		<-done
		t.Fatal("timed-out shutdown released live writer capability")
	}
	if _, err := c.Status(context.Background(), testProject); err == nil {
		close(release)
		<-done
		t.Fatal("new work admitted after shutdown")
	}
	close(release)
	select {
	case err = <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled mutation did not join")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("shutdown did not propagate active-operation cancellation")
	}
	if c.Close(context.Background()) != nil {
		t.Fatal("joined shutdown failed")
	}
	next, err := NewStrictCoordinator(context.Background(), p, []ProjectBinding{{testProject, dir}})
	if err != nil {
		t.Fatal("shutdown failed to release writer after join")
	}
	defer next.Close(context.Background())
	receipt, err := next.Mutate(context.Background(), m, testActor)
	if err != nil || !receipt.Replayed || receipt.ProjectSequence != 1 {
		t.Fatal("cancelled acknowledgement lost or duplicated committed authority")
	}
}

func TestCoordinatorPendingDocumentFencesOnlyDependentWrites(t *testing.T) {
	s, r, dir := readyDocumentStore(t)
	original := documentRequest(t, "document.create", "synthetic-coordinator-pending", authority.EmptyKnowledgeRef, authority.Citation{}, "# Exact Git-only coordinator evidence")
	stop := errors.New("synthetic owned operation interruption")
	s.fault = func(at string) error {
		if at == "knowledge_document_bound" {
			return stop
		}
		return nil
	}
	if _, err := s.MutateDocument(context.Background(), original, testActor); !errors.Is(err, stop) {
		t.Fatal("pending document boundary not reached")
	}
	s.fault = nil
	if s.Close() != nil {
		t.Fatal("pending fixture did not quiesce")
	}
	p, err := r.PrepareRuntime()
	if err != nil {
		t.Fatal("pending fixture preparation failed")
	}
	defer p.Close()
	c, err := NewStrictCoordinator(context.Background(), p, []ProjectBinding{{testProject, dir}})
	if err != nil {
		t.Fatal("pending authority refused coordinator inspection")
	}
	defer c.Close(context.Background())
	state, err := c.KnowledgeStatus(context.Background(), testProject)
	if err != nil || state.Pending == nil {
		t.Fatal("startup hid pending knowledge journal")
	}
	newDoc := original
	newDoc.Intent.IdempotencyKey = "synthetic-dependent-new-key"
	_, err = c.MutateDocument(context.Background(), newDoc, testActor)
	var pending Error
	if !errors.As(err, &pending) || pending.Code != DocumentPending {
		t.Fatal("new dependent write bypassed owned recovery")
	}
	tracker := request(t, "issue.create", "synthetic-independent-tracker", authority.IssuePayload{Title: "Independent tracker", State: "open"})
	if _, err := c.Mutate(context.Background(), tracker, testActor); err != nil {
		t.Fatal("pending knowledge blocked independent tracker")
	}
	result, err := c.MutateDocument(context.Background(), original, testActor)
	if err != nil || !result.Receipt.Replayed || result.Validate() != nil {
		t.Fatal("original document intent could not resume safely")
	}
	state, err = c.KnowledgeStatus(context.Background(), testProject)
	if err != nil || state.Pending != nil || state.Phase != "ready" {
		t.Fatal("successful recovery did not clear dependent fence")
	}
}
