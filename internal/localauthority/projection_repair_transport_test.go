//go:build darwin || linux

package localauthority

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestProjectionRepairHistoryReadPreservesMissingPendingAndUnknown(t *testing.T) {
	c, prepared, dir, req, _, _ := repairApplyFixture(t)
	query := ProjectionRepairPlanRequest{ProjectID: req.ProjectID}
	missing, err := c.ProjectionRepairs(context.Background(), query)
	if err != nil || VerifyProjectionRepairHistory(query, missing) != nil || missing.State != "missing" {
		t.Fatal("missing repair history read failed")
	}
	if _, err := os.Lstat(filepath.Join(dir, repairDirectory)); !os.IsNotExist(err) {
		t.Fatal("history observation provisioned repair metadata")
	}
	q := c.projects[req.ProjectID].repair
	q.fault = func(at string) error {
		if at == "repair_before_quarantine" {
			return context.Canceled
		}
		return nil
	}
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil {
		t.Fatal("prepared history fault did not interrupt effect")
	}
	pending, err := c.ProjectionRepairs(context.Background(), query)
	if err != nil || VerifyProjectionRepairHistory(query, pending) != nil || pending.Generation != 1 || pending.Retained != 1 || pending.Repairs[0].State != "prepared" {
		t.Fatal("pending original repair disappeared from atomic history")
	}
	body, _ := json.Marshal(pending)
	for _, private := range []string{dir, testActor.ID, req.IdempotencyKey, "catalog.json", "synthetic-damaged-metadata", "quarantine-"} {
		if strings.Contains(string(body), private) {
			t.Fatal("repair history disclosed private details")
		}
	}
	if c.Close(context.Background()) != nil || os.Remove(filepath.Join(dir, repairDirectory, "state.json")) != nil {
		t.Fatal("missing-final history fixture failed")
	}
	next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{req.ProjectID, dir}}, false)
	if err != nil {
		t.Fatal("missing repair metadata disabled canonical owner")
	}
	t.Cleanup(func() { _ = next.Close(context.Background()) })
	if _, err := next.ProjectionRepairs(context.Background(), query); err == nil {
		t.Fatal("missing acknowledged repair metadata became empty history")
	}
}

func TestProjectionRepairHistoryRejectsMixedOrOmittedRows(t *testing.T) {
	project := testProject
	first := ProjectionRepairView{Schema: 1, ProjectID: project, RepairID: "11111111-1111-4111-8111-111111111111", PlanID: strings.Repeat("a", 64), Sequence: 1, State: "confirmed", Generation: 3, RestartRequired: true}
	second := first
	second.RepairID, second.PlanID, second.Sequence = "22222222-2222-4222-8222-222222222222", strings.Repeat("b", 64), 2
	history := ProjectionRepairHistory{Schema: 1, ProjectID: project, State: "ready", Generation: 6, Retained: 2, Repairs: []ProjectionRepairView{first, second}}
	history.SnapshotDigest = repairHistoryDigest(history)
	if history.Validate() != nil {
		t.Fatal("valid complete repair history refused")
	}
	for _, change := range []string{"digest", "duplicate-id", "duplicate-plan", "project", "sequence", "phase", "replayed", "generation", "omit", "null", "count"} {
		bad := history
		bad.Repairs = append([]ProjectionRepairView{}, history.Repairs...)
		switch change {
		case "duplicate-id":
			bad.Repairs[1].RepairID = first.RepairID
		case "duplicate-plan":
			bad.Repairs[1].PlanID = first.PlanID
		case "project":
			bad.Repairs[1].ProjectID = "55555555-5555-4555-8555-555555555555"
		case "sequence":
			bad.Repairs[1].Sequence = 0
		case "phase":
			bad.Repairs[0].State, bad.Repairs[0].Generation = "prepared", 1
			bad.Generation = 4
		case "replayed":
			bad.Repairs[1].Replayed = true
		case "generation":
			bad.Generation++
		case "omit":
			bad.Repairs = bad.Repairs[:1]
		case "null":
			bad.Repairs = nil
		case "count":
			bad.Retained++
		}
		if change != "digest" {
			bad.SnapshotDigest = repairHistoryDigest(bad)
		} else {
			bad.SnapshotDigest = strings.Repeat("c", 64)
		}
		if bad.Validate() == nil {
			t.Fatal("repair history accepted mixed or omitted evidence")
		}
	}
	var unknown authority.UUID = "55555555-5555-4555-8555-555555555555"
	if VerifyProjectionRepairHistory(ProjectionRepairPlanRequest{ProjectID: unknown}, history) == nil {
		t.Fatal("repair history crossed project selection")
	}
}
