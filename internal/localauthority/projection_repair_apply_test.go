//go:build darwin || linux

package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func repairApplyFixture(t *testing.T) (*Coordinator, *PreparedRegistry, string, ProjectionRepairRequest, IndexJobRequest, authority.Citation) {
	t.Helper()
	c, prepared, dir := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	created := createIssue(t, p.store, "synthetic-repair-source")
	indexReq, r := admittedIndexRecord(t, c, 1, "synthetic-original-index")
	if c.runIndexRecord(p, r) != nil {
		t.Fatal("actual repair fixture catalog failed")
	}
	if os.WriteFile(filepath.Join(dir, projectionDirectory, "catalog.json"), []byte("synthetic-damaged-metadata"), 0600) != nil {
		t.Fatal("actual corruption fixture failed")
	}
	plan := requireRepairPlan(t, c, "ready")
	return c, prepared, dir, ProjectionRepairRequest{ProjectID: testProject, PlanID: plan.PlanID, IdempotencyKey: "synthetic-original-repair", WriteMode: "local"}, indexReq, created.Citation
}

func requireRepairCanonical(t *testing.T, c *Coordinator, citation authority.Citation, indexReq IndexJobRequest, ledger []byte, dir string) {
	t.Helper()
	read, err := c.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, Operation: "get", EntityID: citation.EntityID})
	if err != nil || len(read.Entities) != 1 || read.Entities[0].Citation != citation {
		t.Fatal("repair changed canonical citation")
	}
	lexical, err := c.LexicalSearch(context.Background(), LexicalRequest{ProjectID: testProject, Query: "synthetic task", Limit: 8})
	if err != nil || len(lexical.Hits) != 1 || lexical.Hits[0].Citation != citation {
		t.Fatal("repair disabled canonical lexical retrieval")
	}
	replay, err := c.EnqueueIndex(context.Background(), indexReq, testActor)
	if err != nil || replay.State != "succeeded" || replay.Attempts != 1 {
		t.Fatal("repair replaced original successful index intent")
	}
	after, err := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
	if err != nil || string(after) != string(ledger) {
		t.Fatal("repair changed retained index ledger bytes")
	}
}

func TestProjectionRepairApplyQuarantinesAndReplaysAcrossOwnerReopen(t *testing.T) {
	c, prepared, dir, req, indexReq, citation := repairApplyFixture(t)
	ledger, err := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
	if err != nil {
		t.Fatal("index history fixture unavailable")
	}
	view, err := c.ApplyProjectionRepair(context.Background(), req, testActor)
	if err != nil || view.Validate() != nil || view.State != "confirmed" || view.Replayed || view.Generation != 3 || !view.RestartRequired {
		t.Fatal("repair did not confirm bounded quarantine")
	}
	if _, err := os.Lstat(filepath.Join(dir, projectionDirectory)); !os.IsNotExist(err) {
		t.Fatal("repair left an active catalog namespace")
	}
	quarantined := filepath.Join(dir, repairDirectory, repairQuarantineName(view.RepairID), "catalog.json")
	body, err := os.ReadFile(quarantined)
	if err != nil || string(body) != "synthetic-damaged-metadata" {
		t.Fatal("quarantine discarded original damaged evidence")
	}
	replayed, err := c.ApplyProjectionRepair(context.Background(), req, testActor)
	if err != nil {
		t.Fatal("confirmed repair did not replay")
	}
	replayed.Replayed = false
	if replayed != view {
		t.Fatal("same-key replay retargeted original repair")
	}
	requireRepairCanonical(t, c, citation, indexReq, ledger, dir)
	newIndex := indexReq
	newIndex.IdempotencyKey = "synthetic-new-index"
	if _, err := c.EnqueueIndex(context.Background(), newIndex, testActor); err == nil {
		t.Fatal("repair bypassed required qualified owner restart")
	}
	if requireRepairPlan(t, c, "blocked").Blocker != "repair_restart_required" {
		t.Fatal("repair hid owner restart boundary")
	}
	encoded, _ := json.Marshal(view)
	for _, secret := range []string{dir, req.IdempotencyKey, testActor.ID, string(body), "catalog.json", "quarantine-"} {
		if secret != "" && strings.Contains(string(encoded), secret) {
			t.Fatal("repair view disclosed protected coordinates or intent")
		}
	}
	if c.Close(context.Background()) != nil {
		t.Fatal("repair owner close failed")
	}
	next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("qualified repair owner reopen failed")
	}
	t.Cleanup(func() { _ = next.Close(context.Background()) })
	next.jobsStarted = true
	requireRepairCanonical(t, next, citation, indexReq, ledger, dir)
	if requireRepairPlan(t, next, "not_needed").RestartRequired {
		t.Fatal("qualified reopen repeated old restart requirement")
	}
	if _, err := next.EnqueueIndex(context.Background(), newIndex, testActor); err != nil {
		t.Fatal("qualified owner could not admit explicit new index intent")
	}
	got, err := next.ProjectionRepair(context.Background(), testProject, view.RepairID)
	if err != nil || got != view {
		t.Fatal("new index changed historical repair receipt")
	}
}

func TestProjectionRepairApplyFaultsRetainOriginalIntent(t *testing.T) {
	type cut struct {
		phase      string
		occurrence int
		barrier    bool
	}
	cuts := []cut{{"repair_namespace_root", 1, true}, {"repair_stage_write", 1, false}, {"repair_stage_file", 1, true}, {"repair_stage_directory", 1, true}, {"repair_stage_ready", 1, false}, {"repair_state_rename", 1, false}, {"repair_state_directory", 1, true}, {"repair_state_acknowledgement", 1, false}, {"repair_before_quarantine", 1, false}, {"repair_quarantine_rename", 1, false}, {"repair_quarantine_file", 1, true}, {"repair_quarantine_directory", 1, true}, {"repair_quarantine_parent", 1, true}, {"repair_quarantine_root", 1, true}, {"repair_quarantine_readback", 1, false}, {"repair_state_rename", 2, false}, {"repair_state_directory", 2, true}, {"repair_state_acknowledgement", 2, false}, {"repair_state_rename", 3, false}, {"repair_state_directory", 3, true}, {"repair_state_acknowledgement", 3, false}, {"repair_receipt_acknowledgement", 1, false}}
	for _, cut := range cuts {
		t.Run(cut.phase+string(rune('0'+cut.occurrence)), func(t *testing.T) {
			c, prepared, dir, req, indexReq, citation := repairApplyFixture(t)
			ledger, _ := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
			q := c.projects[testProject].repair
			seen := 0
			fail := func(at string) error {
				if at == cut.phase {
					seen++
					if seen == cut.occurrence {
						return context.Canceled
					}
				}
				return nil
			}
			if cut.barrier {
				q.barrierFault = fail
			} else {
				q.fault = fail
			}
			if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil || seen < cut.occurrence {
				t.Fatal("repair fault boundary did not fail")
			}
			requireRepairCanonical(t, c, citation, indexReq, ledger, dir)
			if c.Close(context.Background()) != nil {
				t.Fatal("failed repair owner did not join")
			}
			next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
			if err != nil {
				t.Fatal("repair recovery owner reopen failed")
			}
			t.Cleanup(func() { _ = next.Close(context.Background()) })
			next.jobsStarted = true
			if next.projects[testProject].repair == nil {
				// Missing final intent is indistinguishable from loss of an
				// acknowledged final. It is a verified recovery handoff, not an
				// empty ledger or permission for any key to initialize anew.
				var typed Error
				if !errors.As(Error{next.projects[testProject].repairError}, &typed) || typed.Code != RepairUncertain {
					t.Fatal("first-publication ambiguity lost typed recovery boundary")
				}
				for _, key := range []string{req.IdempotencyKey, "synthetic-other-key"} {
					pending := req
					pending.IdempotencyKey = key
					if _, err := next.ApplyProjectionRepair(context.Background(), pending, testActor); err == nil {
						t.Fatal("ambiguous missing final admitted repair permission")
					}
				}
				if _, err := os.Stat(filepath.Join(dir, projectionDirectory, "catalog.json")); err != nil {
					t.Fatal("prepublication handoff moved catalog")
				}
				requireRepairCanonical(t, next, citation, indexReq, ledger, dir)
				return
			}
			if next.projects[testProject].repair.pending() {
				other := req
				other.IdempotencyKey = "synthetic-conflicting-repair"
				if _, err := next.ApplyProjectionRepair(context.Background(), other, testActor); err == nil {
					t.Fatal("new key took over pending repair intent")
				}
			}
			view, err := next.ApplyProjectionRepair(context.Background(), req, testActor)
			if err != nil || view.Validate() != nil || view.State != "confirmed" {
				t.Fatal("original-key repair recovery failed")
			}
			requireRepairCanonical(t, next, citation, indexReq, ledger, dir)
			entries, err := os.ReadDir(filepath.Join(dir, repairDirectory))
			if err != nil {
				t.Fatal("repair evidence inventory unavailable")
			}
			quarantines := 0
			for _, e := range entries {
				if repairQuarantineID(e.Name()).Valid() {
					quarantines++
				}
			}
			if quarantines != 1 || len(next.projects[testProject].repair.state.Records) != 1 {
				t.Fatal("repair replay duplicated effect or original intent")
			}
		})
	}
}

func TestProjectionRepairLostAcknowledgedPreparedFinalNeverBecomesEmpty(t *testing.T) {
	c, prepared, dir, req, indexReq, citation := repairApplyFixture(t)
	q := c.projects[testProject].repair
	q.fault = func(at string) error {
		if at == "repair_before_quarantine" {
			return context.Canceled
		}
		return nil
	}
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil || !q.pending() {
		t.Fatal("acknowledged prepared intent fixture failed")
	}
	ledger, _ := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
	if os.Remove(filepath.Join(dir, repairDirectory, "state.json")) != nil {
		t.Fatal("lost prepared final fixture failed")
	}
	if c.Close(context.Background()) != nil {
		t.Fatal("lost final owner close failed")
	}
	next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("missing repair final disabled canonical owner")
	}
	t.Cleanup(func() { _ = next.Close(context.Background()) })
	next.jobsStarted = true
	if next.projects[testProject].repair != nil || next.projects[testProject].repairError != RepairUncertain {
		t.Fatal("lost acknowledged intent was adopted as unused namespace")
	}
	for _, key := range []string{req.IdempotencyKey, "synthetic-new-key"} {
		other := req
		other.IdempotencyKey = key
		if _, err := next.ApplyProjectionRepair(context.Background(), other, testActor); err == nil {
			t.Fatal("new key took over lost durable repair intent")
		}
	}
	newIndex := indexReq
	newIndex.IdempotencyKey = "synthetic-index-after-lost-repair"
	if _, err := next.EnqueueIndex(context.Background(), newIndex, testActor); err == nil {
		t.Fatal("lost repair intent admitted dependent index work")
	}
	requireRepairCanonical(t, next, citation, indexReq, ledger, dir)
}

func TestProjectionRepairHistoricalRetryReplayPrecedesDependencyFence(t *testing.T) {
	for _, state := range []string{"pending", "confirmed", "corrupt"} {
		t.Run(state, func(t *testing.T) {
			c, prepared, dir := deferredIndexCoordinator(t)
			_, r := admittedIndexRecord(t, c, 0, "synthetic-retried-index")
			cancelled, err := c.CancelIndex(context.Background(), IndexJobAction{ProjectID: testProject, JobID: r.Selection.BuildID, ExpectedGeneration: 1, WriteMode: "local"}, testActor)
			if err != nil {
				t.Fatal("retry replay cancellation fixture failed")
			}
			action := IndexJobAction{ProjectID: testProject, JobID: r.Selection.BuildID, ExpectedGeneration: cancelled.Generation, WriteMode: "local"}
			if _, err := c.RetryIndex(context.Background(), action, testActor); err != nil {
				t.Fatal("original retry permission fixture failed")
			}
			current, found, err := c.projects[testProject].indexJobs.retained(context.Background(), r.Selection.BuildID)
			if err != nil || !found || c.runIndexRecord(c.projects[testProject], current) != nil {
				t.Fatal("retried actual catalog publication failed")
			}
			before := indexControlFixture(t, c, r.Selection.BuildID)
			_, other := admittedIndexRecord(t, c, 0, "synthetic-other-cancelled-index")
			otherCancelled, err := c.CancelIndex(context.Background(), IndexJobAction{ProjectID: testProject, JobID: other.Selection.BuildID, ExpectedGeneration: 1, WriteMode: "local"}, testActor)
			if err != nil {
				t.Fatal("new retry fence fixture failed")
			}
			ledger, _ := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
			if os.WriteFile(filepath.Join(dir, projectionDirectory, "catalog.json"), []byte("{}"), 0600) != nil {
				t.Fatal("retry repair corruption fixture failed")
			}
			req := ProjectionRepairRequest{ProjectID: testProject, PlanID: requireRepairPlan(t, c, "ready").PlanID, IdempotencyKey: "synthetic-retry-repair", WriteMode: "local"}
			if state == "pending" {
				c.projects[testProject].repair.fault = func(at string) error {
					if at == "repair_before_quarantine" {
						return context.Canceled
					}
					return nil
				}
			}
			_, err = c.ApplyProjectionRepair(context.Background(), req, testActor)
			if state == "pending" && err == nil || state != "pending" && err != nil {
				t.Fatal("retry repair state fixture failed")
			}
			if state == "corrupt" {
				if os.WriteFile(filepath.Join(dir, repairDirectory, "state.json"), []byte("{}"), 0600) != nil || c.Close(context.Background()) != nil {
					t.Fatal("retry corrupt repair fixture failed")
				}
				c, err = newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
				if err != nil {
					t.Fatal("retry corrupt repair reopen failed")
				}
				owned := c
				t.Cleanup(func() { _ = owned.Close(context.Background()) })
				c.jobsStarted = true
			}
			for _, expected := range []uint64{action.ExpectedGeneration, before.Generation} {
				replayed := action
				replayed.ExpectedGeneration = expected
				got, err := c.RetryIndex(context.Background(), replayed, testActor)
				if err != nil || got != before {
					t.Fatal("repair fence blocked already acknowledged retry outcome")
				}
			}
			fresh := IndexJobAction{ProjectID: testProject, JobID: other.Selection.BuildID, ExpectedGeneration: otherCancelled.Generation, WriteMode: "local"}
			if _, err := c.RetryIndex(context.Background(), fresh, testActor); err == nil {
				t.Fatal("repair dependency fence admitted genuinely new retry")
			}
			after, _ := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
			if string(after) != string(ledger) {
				t.Fatal("historical retry replay or repair changed retained index bytes")
			}
		})
	}
}

type repairInspectionCancellation struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int32
	at     int32
}

func (c *repairInspectionCancellation) Err() error {
	if c.checks.Add(1) == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

func TestProjectionRepairInspectionCancellationIsNotCorruption(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "already-moved"}[moved], func(t *testing.T) {
			c, _, dir, req, indexReq, citation := repairApplyFixture(t)
			q := c.projects[testProject].repair
			q.fault = func(at string) error {
				if !moved && at == "repair_before_quarantine" || moved && at == "repair_quarantine_rename" {
					return context.Canceled
				}
				return nil
			}
			if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil {
				t.Fatal("inspection cancellation phase fixture failed")
			}
			ledger, _ := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
			before, _ := os.ReadFile(filepath.Join(dir, repairDirectory, "state.json"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			interrupt := &repairInspectionCancellation{Context: ctx, cancel: cancel, at: 2}
			var err error
			if moved {
				err = q.verifyQuarantine(interrupt, q.state.Records[0], true)
			} else {
				err = c.resumeProjectionRepair(interrupt, c.projects[testProject], 0)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatal("mid-inspection cancellation was classified as corruption")
			}
			after, _ := os.ReadFile(filepath.Join(dir, repairDirectory, "state.json"))
			if string(after) != string(before) {
				t.Fatal("cancelled inspection changed durable repair phase")
			}
			if moved && q.fence != RepairUncertain {
				t.Fatal("cancelled post-effect inspection cleared original uncertainty fence")
			}
			requireRepairCanonical(t, c, citation, indexReq, ledger, dir)
		})
	}
}

func TestProjectionRepairConfirmedKeyReplaysThroughUnrelatedIndexCorruption(t *testing.T) {
	c, prepared, dir, req, _, citation := repairApplyFixture(t)
	view, err := c.ApplyProjectionRepair(context.Background(), req, testActor)
	if err != nil || view.State != "confirmed" {
		t.Fatal("confirmed repair fixture failed")
	}
	repairState, _ := os.ReadFile(filepath.Join(dir, repairDirectory, "state.json"))
	indexPath := filepath.Join(dir, indexJobsDirectory, "state.json")
	if os.WriteFile(indexPath, []byte("{}"), 0600) != nil || c.Close(context.Background()) != nil {
		t.Fatal("unrelated damaged index fixture failed")
	}
	next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("damaged index disabled canonical owner")
	}
	t.Cleanup(func() { _ = next.Close(context.Background()) })
	next.jobsStarted = true
	if next.projects[testProject].indexJobs != nil || next.projects[testProject].repair == nil {
		t.Fatal("damaged-index and valid-repair boundaries were not independent")
	}
	known, err := next.ProjectionRepair(context.Background(), testProject, view.RepairID)
	if err != nil || known != view {
		t.Fatal("verified historical repair receipt unavailable")
	}
	replay, err := next.ApplyProjectionRepair(context.Background(), req, testActor)
	if err != nil || !replay.Replayed {
		t.Fatal("unrelated index corruption blocked confirmed original-key replay")
	}
	replay.Replayed = false
	if replay != view {
		t.Fatal("confirmed key replay changed original repair effect")
	}
	fresh := req
	fresh.IdempotencyKey = "synthetic-new-repair-after-index-corruption"
	if _, err := next.ApplyProjectionRepair(context.Background(), fresh, testActor); err == nil {
		t.Fatal("index corruption admitted a genuinely new repair effect")
	}
	after, _ := os.ReadFile(filepath.Join(dir, repairDirectory, "state.json"))
	damaged, _ := os.ReadFile(indexPath)
	if string(after) != string(repairState) || string(damaged) != "{}" {
		t.Fatal("historical repair replay changed independent durable evidence")
	}
	read, err := next.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, Operation: "get", EntityID: citation.EntityID})
	if err != nil || len(read.Entities) != 1 || read.Entities[0].Citation != citation {
		t.Fatal("historical repair replay changed canonical citation")
	}
}

func TestProjectionRepairApplyRejectsStaleAndConflictingRequests(t *testing.T) {
	c, _, dir, req, _, _ := repairApplyFixture(t)
	if os.WriteFile(filepath.Join(dir, projectionDirectory, "catalog.json"), []byte("different-damaged-image"), 0600) != nil {
		t.Fatal("stale image fixture failed")
	}
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); !authorityConflict(err) {
		t.Fatal("stale plan authorized changed bytes")
	}
	if _, err := os.Lstat(filepath.Join(dir, repairDirectory)); !os.IsNotExist(err) {
		t.Fatal("stale plan provisioned repair intent")
	}
	req.PlanID = requireRepairPlan(t, c, "ready").PlanID
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err != nil {
		t.Fatal("fresh repair failed")
	}
	changed := req
	changed.PlanID = hash([]byte("different-plan"))
	if _, err := c.ApplyProjectionRepair(context.Background(), changed, testActor); !authorityConflict(err) {
		t.Fatal("same key accepted a different plan")
	}
	otherActor := testActor
	otherActor.ID = "synthetic-other-actor"
	if _, err := c.ApplyProjectionRepair(context.Background(), req, otherActor); !authorityConflict(err) {
		t.Fatal("same key accepted a changed actor")
	}
	invalid := req
	invalid.WriteMode = "live"
	if _, err := c.ApplyProjectionRepair(context.Background(), invalid, testActor); err == nil {
		t.Fatal("remote write mode authorized local repair")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ApplyProjectionRepair(ctx, req, testActor); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request obtained repair receipt")
	}
}

func TestProjectionRepairCancelledIndexIntentIsNeverRevived(t *testing.T) {
	c, prepared, dir := deferredIndexCoordinator(t)
	indexReq, r := admittedIndexRecord(t, c, 0, "synthetic-cancelled-index")
	before, err := c.CancelIndex(context.Background(), IndexJobAction{ProjectID: testProject, JobID: r.Selection.BuildID, ExpectedGeneration: 1, WriteMode: "local"}, testActor)
	if err != nil || before.State != "cancelled" {
		t.Fatal("actual index cancellation fixture failed")
	}
	ledger, _ := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
	if os.Mkdir(filepath.Join(dir, projectionDirectory), 0700) != nil || os.WriteFile(filepath.Join(dir, projectionDirectory, "catalog.json"), []byte("{}"), 0600) != nil {
		t.Fatal("cancelled orphan catalog fixture failed")
	}
	req := ProjectionRepairRequest{ProjectID: testProject, PlanID: requireRepairPlan(t, c, "ready").PlanID, WriteMode: "local", IdempotencyKey: "synthetic-cancelled-repair"}
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err != nil {
		t.Fatal("cancelled intent repair failed")
	}
	if c.Close(context.Background()) != nil {
		t.Fatal("cancelled intent owner did not close")
	}
	next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("cancelled intent recovery reopen failed")
	}
	t.Cleanup(func() { _ = next.Close(context.Background()) })
	next.jobsStarted = true
	replay, err := next.EnqueueIndex(context.Background(), indexReq, testActor)
	after, _ := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
	if err != nil || replay != before || string(after) != string(ledger) {
		t.Fatal("repair revived or discarded original cancellation intent")
	}
}

func TestProjectionRepairPreparedIntentDoesNotRetargetAdvancedCanonicalContent(t *testing.T) {
	c, _, _, req, _, citation := repairApplyFixture(t)
	q := c.projects[testProject].repair
	q.fault = func(at string) error {
		if at == "repair_before_quarantine" {
			return context.Canceled
		}
		return nil
	}
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); !errors.Is(err, context.Canceled) {
		t.Fatal("prepared repair fixture did not stop before effect")
	}
	created := createIssue(t, c.projects[testProject].store, "synthetic-source-advanced-during-pending-repair")
	q.fault = nil
	view, err := c.ApplyProjectionRepair(context.Background(), req, testActor)
	if err != nil || !view.Replayed || view.Sequence != 1 || view.State != "confirmed" {
		t.Fatal("prepared replay retargeted its original source selector")
	}
	status, err := c.Status(context.Background(), testProject)
	if err != nil || status.AuthorityGeneration != created.ProjectSequence || status.AuthorityGeneration != 2 {
		t.Fatal("repair changed newer canonical generation")
	}
	for _, want := range []authority.Citation{citation, created.Citation} {
		read, err := c.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, Operation: "get", EntityID: want.EntityID})
		if err != nil || len(read.Entities) != 1 || read.Entities[0].Citation != want {
			t.Fatal("repair replaced immutable canonical content")
		}
	}
}

func TestProjectionRepairConservesCausalModelAndClosedViews(t *testing.T) {
	c, _, _, req, _, _ := repairApplyFixture(t)
	q := c.projects[testProject].repair
	q.fault = func(at string) error {
		if at == "repair_before_quarantine" {
			return context.Canceled
		}
		return nil
	}
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil {
		t.Fatal("prepared causal fixture failed")
	}
	before := q.state
	next := before
	next.Records = append([]projectionRepairRecord(nil), before.Records...)
	next.Generation++
	next.Records[0].Generation++
	next.Records[0].State = "quarantined"
	if !repairSnapshotTransition(before, next) {
		t.Fatal("one conserved phase transition was rejected")
	}
	mutations := []func(*projectionRepairRecord){func(r *projectionRepairRecord) { r.KeyHash = hash([]byte("other-key")) }, func(r *projectionRepairRecord) { r.ActorHash = hash([]byte("other-actor")) }, func(r *projectionRepairRecord) { r.RootIdentity = hash([]byte("other-root")) }, func(r *projectionRepairRecord) { r.ProjectionFingerprint = hash([]byte("other-image")) }, func(r *projectionRepairRecord) { r.IndexDigest = hash([]byte("other-index")) }}
	for _, mutate := range mutations {
		invalid := next
		invalid.Records = append([]projectionRepairRecord(nil), next.Records...)
		mutate(&invalid.Records[0])
		if repairSnapshotTransition(before, invalid) {
			t.Fatal("phase update changed immutable repair intent")
		}
	}
	wrong := next
	wrong.Records = append([]projectionRepairRecord(nil), next.Records...)
	wrong.Records[0].IntentHash = hash([]byte("unbound-intent"))
	if wrong.valid() {
		t.Fatal("durable repair accepted unbound plan intent")
	}
	v := repairView(before.Records[0], false)
	for _, mutate := range []func(*ProjectionRepairView){func(v *ProjectionRepairView) { v.State = "finished" }, func(v *ProjectionRepairView) { v.Generation = 3 }, func(v *ProjectionRepairView) { v.RestartRequired = false }, func(v *ProjectionRepairView) { v.Bytes = -1 }, func(v *ProjectionRepairView) { v.Files = 100 }} {
		invalid := v
		mutate(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("protected repair view admitted impossible effects")
		}
	}
}

func TestProjectionRepairExclusiveRenameNeverReplacesDestination(t *testing.T) {
	c, _, dir, req, _, _ := repairApplyFixture(t)
	q := c.projects[testProject].repair
	q.fault = func(at string) error {
		if at != "repair_before_quarantine" {
			return nil
		}
		r := q.state.Records[0]
		if os.Mkdir(filepath.Join(dir, repairDirectory, repairQuarantineName(r.ID)), 0700) != nil {
			t.Fatal("exclusive destination fixture failed")
		}
		return nil
	}
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil {
		t.Fatal("repair replaced a preexisting destination")
	}
	if _, err := os.Stat(filepath.Join(dir, projectionDirectory, "catalog.json")); err != nil {
		t.Fatal("no-replace failure moved source catalog")
	}
	if q.state.Records[0].State != "prepared" {
		t.Fatal("no-replace failure confirmed a repair effect")
	}
}

func TestProjectionRepairRejectsUnsafeMetadataNamespace(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "mode", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			c, _, dir, req, _, _ := repairApplyFixture(t)
			ns := filepath.Join(dir, repairDirectory)
			switch kind {
			case "symlink":
				if os.Symlink(filepath.Join(dir, projectionDirectory), ns) != nil {
					t.Fatal("unsafe symlink fixture failed")
				}
			case "fifo":
				if unix.Mkfifo(ns, 0600) != nil {
					t.Fatal("unsafe pipe fixture failed")
				}
			default:
				if os.Mkdir(ns, 0700) != nil {
					t.Fatal("unsafe namespace fixture failed")
				}
				if kind == "mode" {
					if os.Chmod(ns, 0755) != nil {
						t.Fatal("unsafe mode fixture failed")
					}
				} else if os.WriteFile(filepath.Join(ns, "unknown"), []byte("{}"), 0600) != nil {
					t.Fatal("unsafe object fixture failed")
				}
			}
			if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err == nil {
				t.Fatal("repair adopted an unadmitted namespace")
			}
			if _, err := os.Stat(filepath.Join(dir, projectionDirectory, "catalog.json")); err != nil {
				t.Fatal("unsafe metadata failure changed source projection")
			}
		})
	}
}

func TestProjectionRepairShutdownKeepsWriterUntilApplyJoins(t *testing.T) {
	c, prepared, dir, req, _, _ := repairApplyFixture(t)
	entered, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	c.projects[testProject].repair.fault = func(at string) error {
		if at == "repair_before_quarantine" {
			close(entered)
			<-resume
		}
		return nil
	}
	result := make(chan error, 1)
	go func() { _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); result <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("owned repair did not enter bounded test barrier")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if !errors.Is(c.Close(ctx), context.DeadlineExceeded) {
		t.Fatal("owner closed before repair lifetime joined")
	}
	if second, err := prepared.OpenWriter(context.Background(), dir, testProject); err == nil {
		_ = second.Close()
		t.Fatal("interrupted apply released writer before join")
	}
	close(resume)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("shutdown did not cancel owned repair")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled repair did not join")
	}
	if c.Close(context.Background()) != nil {
		t.Fatal("joined owner did not close")
	}
	if _, err := os.Stat(filepath.Join(dir, projectionDirectory, "catalog.json")); err != nil {
		t.Fatal("pre-effect cancellation moved catalog")
	}
	next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("joined repair recovery could not reopen")
	}
	t.Cleanup(func() { _ = next.Close(context.Background()) })
	next.jobsStarted = true
	if requireRepairPlan(t, next, "blocked").Blocker != "repair_pending" {
		t.Fatal("reopen hid durable pending repair")
	}
	if _, err := next.ApplyProjectionRepair(context.Background(), req, testActor); err != nil {
		t.Fatal("original repair key could not resume after shutdown")
	}
}

func TestProjectionRepairQuotaPreservesEveryQuarantineAndIntent(t *testing.T) {
	c, prepared, dir, req, _, _ := repairApplyFixture(t)
	for i := 0; i < maxProjectionRepairs; i++ {
		if i > 0 {
			if c.Close(context.Background()) != nil {
				t.Fatal("quota fixture owner close failed")
			}
			var err error
			c, err = newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
			if err != nil {
				t.Fatal("quota fixture owner reopen failed")
			}
			owned := c
			t.Cleanup(func() { _ = owned.Close(context.Background()) })
			c.jobsStarted = true
			if os.Mkdir(filepath.Join(dir, projectionDirectory), 0700) != nil || os.WriteFile(filepath.Join(dir, projectionDirectory, "catalog.json"), []byte("{}"), 0600) != nil {
				t.Fatal("bounded repeated-corruption fixture failed")
			}
			req.PlanID = requireRepairPlan(t, c, "ready").PlanID
			req.IdempotencyKey += "x"
		}
		if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err != nil {
			t.Fatal("bounded repair admission failed before quota")
		}
	}
	q := c.projects[testProject].repair
	before, err := os.ReadFile(filepath.Join(dir, repairDirectory, "state.json"))
	if err != nil {
		t.Fatal("quota intent evidence unavailable")
	}
	more := req
	more.IdempotencyKey += "x"
	if _, err := c.ApplyProjectionRepair(context.Background(), more, testActor); err == nil {
		t.Fatal("quota admitted unbounded new repair")
	}
	after, err := os.ReadFile(filepath.Join(dir, repairDirectory, "state.json"))
	if err != nil || string(before) != string(after) || len(q.state.Records) != maxProjectionRepairs {
		t.Fatal("quota failure discarded durable repair history")
	}
	for _, r := range q.state.Records {
		if r.State != "confirmed" || q.verifyQuarantine(context.Background(), r, false) != nil {
			t.Fatal("quota failure removed a confirmed recovery point")
		}
	}
}

func TestProjectionRepairCorruptLedgerOrQuarantineNeverWeakensCanonicalReads(t *testing.T) {
	for _, target := range []string{"ledger", "quarantine", "unrecorded", "hardlink"} {
		t.Run(target, func(t *testing.T) {
			c, prepared, dir, req, indexReq, citation := repairApplyFixture(t)
			view, err := c.ApplyProjectionRepair(context.Background(), req, testActor)
			if err != nil {
				t.Fatal("repair history fixture failed")
			}
			ledger, _ := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
			path := filepath.Join(dir, repairDirectory, "state.json")
			switch target {
			case "ledger":
				if os.WriteFile(path, []byte("{}"), 0600) != nil {
					t.Fatal("corrupt repair ledger fixture failed")
				}
			case "quarantine":
				if os.WriteFile(filepath.Join(dir, repairDirectory, repairQuarantineName(view.RepairID), "catalog.json"), []byte("changed"), 0600) != nil {
					t.Fatal("corrupt quarantine fixture failed")
				}
			case "unrecorded":
				id, _ := authority.NewUUID()
				if os.Mkdir(filepath.Join(dir, repairDirectory, repairQuarantineName(id)), 0700) != nil {
					t.Fatal("unrecorded quarantine fixture failed")
				}
			case "hardlink":
				if os.Link(path, filepath.Join(dir, "synthetic-hardlink")) != nil {
					t.Fatal("unsafe repair ledger alias fixture failed")
				}
			}
			if c.Close(context.Background()) != nil {
				t.Fatal("repair corruption fixture owner close failed")
			}
			next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
			if err != nil {
				t.Fatal("repair failure disabled canonical runtime")
			}
			t.Cleanup(func() { _ = next.Close(context.Background()) })
			next.jobsStarted = true
			if next.projects[testProject].repair != nil {
				t.Fatal("corrupt repair history obtained writable repair ownership")
			}
			if _, err := next.ApplyProjectionRepair(context.Background(), req, testActor); err == nil {
				t.Fatal("corrupt repair history returned a successful receipt")
			}
			newIndex := indexReq
			newIndex.IdempotencyKey = "synthetic-after-corrupt-repair"
			if _, err := next.EnqueueIndex(context.Background(), newIndex, testActor); err == nil {
				t.Fatal("damaged repair intent admitted dependent new work")
			}
			requireRepairCanonical(t, next, citation, indexReq, ledger, dir)
		})
	}
}
