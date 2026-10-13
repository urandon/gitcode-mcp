package localauthority

import (
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
