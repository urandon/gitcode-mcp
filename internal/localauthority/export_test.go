package localauthority

import (
	"context"
	"testing"

	"gitcode-mcp/internal/authority"
)

// NewFixtureRegistry exists only in augmented test builds so external native
// transport tests can use an isolated ownership root. No production path/FD
// constructor is introduced and no operator registry is touched.
func NewFixtureRegistry(root string) (*Registry, error) { return newRegistry(root) }

// Recovery transport controls exist only in augmented test builds. They
// construct a synthetic interrupted source and defer its real worker so the
// external IPC cohort can exercise durable queued cancellation deterministically.
func NewRecoveryTransportFixture(t *testing.T) (*Coordinator, RecoveryJobRequest, authority.DocumentMutationRequest) {
	t.Helper()
	c, _, _, o, req := recoveryCoordinatorActorFixture(t, "knowledge_document_bound", Actor{ID: "local-user"})
	return c, RecoveryJobRequest{ProjectID: testProject, OperationID: o.ID, ObservedGeneration: o.Generation, WriteMode: "local"}, req
}

func StartRecoveryTransportFixtureWorkers(c *Coordinator) error {
	c.mu.Lock()
	c.jobsStarted = false
	c.mu.Unlock()
	return c.activateRecoveryWorkers()
}

// Test-only prepared owner with deferred dispatch permits deterministic queued
// cancellation/reopen over actual metadata. No fixture constructor is exported
// by production binaries and no operator state is touched.
func NewIndexTransportFixture(t *testing.T) (*Coordinator, IndexJobRequest, func() *Coordinator) {
	t.Helper()
	c, prepared, dir := deferredIndexCoordinator(t)
	if _, err := c.Mutate(context.Background(), request(t, "issue.create", "synthetic-index-ipc-source", authority.IssuePayload{Title: "Synthetic IPC source", Body: "canonical source sentinel", State: "open"}), Actor{ID: "local-user"}); err != nil {
		t.Fatal("index transport canonical fixture unavailable")
	}
	reopen := func() *Coordinator {
		next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
		if err != nil {
			t.Fatal("index transport owner reopen failed")
		}
		next.jobsStarted = true
		t.Cleanup(func() {
			if next.Close(context.Background()) != nil {
				t.Error("reopened index transport owner did not join")
			}
		})
		return next
	}
	return c, IndexJobRequest{ProjectID: testProject, Sequence: 1, IdempotencyKey: "synthetic-index-ipc-original", WriteMode: "local"}, reopen
}
