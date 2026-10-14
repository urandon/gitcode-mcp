//go:build darwin || linux

package localauthority

import (
	"context"
	"testing"

	"gitcode-mcp/internal/authority"
)

// Actual damaged derived catalog and lost acknowledgement, only in augmented
// tests. The production API has no path selector, fixture or fault control.
func NewProjectionRepairTransportFixture(t *testing.T) (*Coordinator, ProjectionRepairRequest, authority.Citation, func() *Coordinator) {
	t.Helper()
	c, prepared, dir, req, _, citation := repairApplyFixture(t)
	c.projects[testProject].repair.fault = func(at string) error {
		if at == "repair_receipt_acknowledgement" {
			return context.Canceled
		}
		return nil
	}
	reopen := func() *Coordinator {
		next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
		if err != nil {
			t.Fatal("repair transport owner reopen failed")
		}
		next.jobsStarted = true
		t.Cleanup(func() { _ = next.Close(context.Background()) })
		return next
	}
	return c, req, citation, reopen
}
