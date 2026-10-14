//go:build darwin || linux

package localauthority_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/servicectl"
)

// Startup reconciliation may briefly own the project turn. Only observation
// retries that typed conflict; no repair/index write is retried here.
func observeNativeRepair(ctx context.Context, client *servicectl.RPCClient, method string, input, output any) error {
	for {
		err := client.Call(ctx, method, input, output)
		var domain authority.Error
		if !errors.As(err, &domain) || domain.Code != authority.Conflict {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func TestProjectionRepairIPCActualLostAckHistoryReplayAndReopen(t *testing.T) {
	c, req, citation, reopen := localauthority.NewProjectionRepairTransportFixture(t)
	client := recoveryIPCClient(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	query := localauthority.ProjectionRepairPlanRequest{ProjectID: req.ProjectID}
	var history localauthority.ProjectionRepairHistory
	if client.Call(ctx, "AuthorityV1.ProjectionRepairs", query, &history) != nil || history.State != "missing" || history.Retained != 0 || localauthority.VerifyProjectionRepairHistory(query, history) != nil {
		t.Fatal("private missing repair history failed")
	}
	var planned localauthority.ProjectionRepairPlanResult
	if client.Call(ctx, "AuthorityV1.PlanProjectionRepair", query, &planned) != nil || planned.Plan.State != "ready" || planned.Plan.PlanID != req.PlanID || localauthority.VerifyProjectionRepairPlan(query, planned) != nil {
		t.Fatal("actual private repair planning failed")
	}
	bad := req
	bad.WriteMode = "live"
	var domain authority.Error
	if err := client.Call(ctx, "AuthorityV1.ApplyProjectionRepair", bad, nil); !errors.As(err, &domain) || domain.Code != authority.InvalidContract {
		t.Fatal("private repair accepted remote write mode")
	}
	// The actual effect commits, but the acknowledgement is deliberately lost.
	// Client does not retry the write or claim rollback. The live owner remains
	// fenced; qualified reopen is required before discovering the exact effect.
	err := client.Call(ctx, "AuthorityV1.ApplyProjectionRepair", req, nil)
	if !errors.As(err, &domain) || domain.Code != authority.Unavailable {
		t.Fatal("lost repair acknowledgement became success or raw error")
	}
	var storage localauthority.Error
	if err := client.Call(ctx, "AuthorityV1.ProjectionRepairs", query, &history); !errors.As(err, &storage) || storage.Code != localauthority.RepairUncertain || history.State != "missing" {
		t.Fatal("ambiguous live repair returned unfenced or partially replaced history")
	}
	index := localauthority.IndexJobRequest{ProjectID: req.ProjectID, Sequence: planned.Plan.Sequence, IdempotencyKey: "synthetic-post-repair-index", WriteMode: "local"}
	if err := client.Call(ctx, "AuthorityV1.EnqueueIndex", index, nil); !errors.As(err, &storage) || storage.Code != localauthority.RepairUncertain {
		t.Fatal("ambiguous live repair admitted new dependent index work")
	}
	if c.Close(ctx) != nil {
		t.Fatal("ambiguous repair IPC owner did not join")
	}
	recovered := reopen()
	client = recoveryIPCClient(t, recovered)
	if client.Call(ctx, "AuthorityV1.ProjectionRepairs", query, &history) != nil || history.State != "ready" || history.Generation != 3 || history.Retained != 1 || history.Repairs[0].State != "confirmed" {
		t.Fatal("qualified lost-ack recovery erased confirmed repair discovery")
	}
	var confirmation localauthority.ProjectionRepairConfirmation
	if client.Call(ctx, "AuthorityV1.ApplyProjectionRepair", req, &confirmation) != nil || !confirmation.Repair.Replayed || localauthority.VerifyProjectionRepairConfirmation(req, confirmation) != nil {
		t.Fatal("original private repair request did not safely replay")
	}
	lookup := localauthority.ProjectionRepairLookup{ProjectID: req.ProjectID, RepairID: confirmation.Repair.RepairID}
	var retained localauthority.ProjectionRepairView
	if client.Call(ctx, "AuthorityV1.ProjectionRepair", lookup, &retained) != nil || !reflect.DeepEqual(retained, history.Repairs[0]) {
		t.Fatal("private repair lookup changed historical effect")
	}
	// Reopen already qualified the original possible effect. Replaying the
	// receipt does not create a new restart requirement in that new owner.
	if client.Call(ctx, "AuthorityV1.PlanProjectionRepair", query, &planned) != nil || planned.Plan.State != "not_needed" {
		t.Fatal("qualified recovery invented a new repair permission")
	}
	if recovered.Close(ctx) != nil {
		t.Fatal("repair IPC owner did not join")
	}
	next := reopen()
	client = recoveryIPCClient(t, next)
	var reopened localauthority.ProjectionRepairHistory
	if client.Call(ctx, "AuthorityV1.ProjectionRepairs", query, &reopened) != nil || !reflect.DeepEqual(reopened, history) {
		t.Fatal("qualified owner reopen lost exact bounded repair history")
	}
	if client.Call(ctx, "AuthorityV1.ApplyProjectionRepair", req, &confirmation) != nil || !confirmation.Repair.Replayed {
		t.Fatal("qualified owner reopen lost original repair intent")
	}
	var read authority.ReadResult
	if client.Call(ctx, "AuthorityV1.Read", authority.ReadRequest{ProjectID: req.ProjectID, Kind: citation.Kind, EntityID: citation.EntityID, Operation: "get"}, &read) != nil || len(read.Entities) != 1 || read.Entities[0].Citation != citation {
		t.Fatal("repair changed canonical source citation")
	}
	var search localauthority.SearchResult
	if client.Call(ctx, "AuthorityV1.Search", localauthority.SearchRequest{ProjectID: req.ProjectID, Query: "synthetic task", Mode: "hybrid", Limit: 8}, &search) != nil || search.EffectiveMode != "full_text" || search.FallbackReason != "policy_denied" || len(search.Hits) != 1 || search.Hits[0].Citation != citation {
		t.Fatal("repair damaged lexical retrieval or invented semantic readiness")
	}
	var admission localauthority.IndexJobAdmission
	if client.Call(ctx, "AuthorityV1.EnqueueIndex", index, &admission) != nil || localauthority.VerifyIndexJobAdmission(index, admission) != nil {
		t.Fatal("qualified owner could not admit separate explicit index intent")
	}
	if client.Call(ctx, "AuthorityV1.ProjectionRepairs", query, &reopened) != nil || !reflect.DeepEqual(reopened, history) {
		t.Fatal("new index admission rewrote historical repair discovery")
	}
}

func TestNativeProjectionRepairActualQuarantineAndProcessReopen(t *testing.T) {
	p, _, dir := nativeLaunchFixture(t)
	first := nativeProcess(t, p, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var receipt authority.MutationReceipt
	if first.Client().Call(ctx, "AuthorityV1.Mutate", nativeMutation("synthetic-native-repair-source", "Native repair source"), &receipt) != nil {
		t.Fatal("native repair canonical source failed")
	}
	index := localauthority.IndexJobRequest{ProjectID: fixtureProject, Sequence: receipt.ProjectSequence, IdempotencyKey: "synthetic-native-repair-index", WriteMode: "local"}
	var admitted localauthority.IndexJobAdmission
	if first.Client().Call(ctx, "AuthorityV1.EnqueueIndex", index, &admitted) != nil {
		t.Fatal("native repair fixture index admission failed")
	}
	lookup := localauthority.IndexJobLookup{ProjectID: fixtureProject, JobID: admitted.Job.JobID}
	var indexed localauthority.IndexJobView
	for {
		err := first.Client().Call(ctx, "AuthorityV1.IndexJob", lookup, &indexed)
		var domain authority.Error
		if err != nil && (!errors.As(err, &domain) || domain.Code != authority.Conflict) {
			t.Fatal("native repair fixture index observation failed")
		}
		if err == nil && indexed.State == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("native repair fixture index did not settle")
		case <-time.After(time.Millisecond):
		}
	}
	if !indexed.Published || first.Close(ctx) != nil {
		t.Fatal("native repair initial catalog owner did not join")
	}
	// Deliberate corruption is limited to the fixed derived fixture after its
	// actual native writer joins. No authority byte or operator cache is touched.
	if os.WriteFile(filepath.Join(dir, "runtime-projection", "catalog.json"), []byte("synthetic-native-damaged-catalog"), 0600) != nil {
		t.Fatal("native repair damaged-catalog fixture failed")
	}
	damaged := nativeProcess(t, p, dir)
	query := localauthority.ProjectionRepairPlanRequest{ProjectID: fixtureProject}
	var plan localauthority.ProjectionRepairPlanResult
	if observeNativeRepair(ctx, damaged.Client(), "AuthorityV1.PlanProjectionRepair", query, &plan) != nil || plan.Plan.State != "ready" {
		t.Fatal("native repair proposal failed")
	}
	req := localauthority.ProjectionRepairRequest{ProjectID: fixtureProject, PlanID: plan.Plan.PlanID, IdempotencyKey: "synthetic-native-repair-original", WriteMode: "local"}
	var confirmed localauthority.ProjectionRepairConfirmation
	if damaged.Client().Call(ctx, "AuthorityV1.ApplyProjectionRepair", req, &confirmed) != nil || localauthority.VerifyProjectionRepairConfirmation(req, confirmed) != nil || confirmed.Repair.Replayed {
		t.Fatal("native quarantine did not confirm actual first effect")
	}
	var history localauthority.ProjectionRepairHistory
	if damaged.Client().Call(ctx, "AuthorityV1.ProjectionRepairs", query, &history) != nil || history.Retained != 1 || history.Generation != 3 || damaged.Close(ctx) != nil {
		t.Fatal("native quarantine history/join failed")
	}
	reopened := nativeProcess(t, p, dir)
	var after localauthority.ProjectionRepairHistory
	if observeNativeRepair(ctx, reopened.Client(), "AuthorityV1.ProjectionRepairs", query, &after) != nil || !reflect.DeepEqual(history, after) {
		t.Fatal("native repair restart lost original history")
	}
	if reopened.Client().Call(ctx, "AuthorityV1.ApplyProjectionRepair", req, &confirmed) != nil || !confirmed.Repair.Replayed {
		t.Fatal("native repair restart did not replay exact original key")
	}
	var oldIndex localauthority.IndexJobAdmission
	if reopened.Client().Call(ctx, "AuthorityV1.EnqueueIndex", index, &oldIndex) != nil || !reflect.DeepEqual(indexed, oldIndex.Job) {
		t.Fatal("native repair rewrote original completed index intent")
	}
	var read authority.ReadResult
	if reopened.Client().Call(ctx, "AuthorityV1.Read", authority.ReadRequest{ProjectID: fixtureProject, Kind: authority.Issue, EntityID: receipt.Citation.EntityID, Operation: "get"}, &read) != nil || len(read.Entities) != 1 || read.Entities[0].Citation != receipt.Citation {
		t.Fatal("native quarantine changed canonical source receipt")
	}
}
