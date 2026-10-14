//go:build darwin || linux

package localauthority

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewPostMoveDurabilityFailureSealsLiveRepairOwner(t *testing.T) {
	c, _, dir, req, _, _ := repairApplyFixture(t)
	q := c.projects[testProject].repair
	q.barrierFault = func(at string) error {
		if at == "repair_quarantine_file" {
			return context.Canceled
		}
		return nil
	}
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil {
		t.Fatal("post-move durability control did not fail")
	}
	if _, err := os.Stat(filepath.Join(dir, projectionDirectory)); !os.IsNotExist(err) {
		t.Fatal("control did not reach actual namespace move")
	}
	if !q.pending() || q.state.Records[0].State != "prepared" {
		t.Fatal("failed proof incorrectly advanced durable intent")
	}
	if q.fence != RepairUncertain {
		t.Fatal("post-move durability failure did not fence the live owner")
	}
}

func TestReviewPostMoveDurabilityFailureRequiresQualifiedReopen(t *testing.T) {
	c, _, _, req, _, _ := repairApplyFixture(t)
	q := c.projects[testProject].repair
	q.barrierFault = func(at string) error {
		if at == "repair_quarantine_root" {
			return context.Canceled
		}
		return nil
	}
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil {
		t.Fatal("post-move root durability control did not fail")
	}
	q.barrierFault = nil
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil {
		t.Fatal("uncertain live owner resumed effect without qualified reopen")
	}
}
