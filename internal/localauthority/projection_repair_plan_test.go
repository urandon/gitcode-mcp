//go:build darwin || linux

package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func repairPlanFixture(t *testing.T) (*Coordinator, string) {
	t.Helper()
	c, _, dir := deferredIndexCoordinator(t)
	return c, dir
}

func requireRepairPlan(t *testing.T, c *Coordinator, state string) ProjectionRepairPlan {
	t.Helper()
	p, err := c.PlanProjectionRepair(context.Background(), testProject)
	if err != nil || p.Validate() != nil || p.State != state {
		t.Fatal("repair plan did not report its exact expected state")
	}
	return p
}

func TestProjectionRepairPlanMissingAndValidNeverProvision(t *testing.T) {
	c, dir := repairPlanFixture(t)
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal("repair fixture inventory unavailable")
	}
	missing := requireRepairPlan(t, c, "not_needed")
	if missing.Files != 0 || missing.Bytes != 0 || missing.LedgerState != "missing" || missing.LedgerGeneration != 0 || missing.RestartRequired {
		t.Fatal("missing plan fabricated durable index state")
	}
	if missing != requireRepairPlan(t, c, "not_needed") {
		t.Fatal("unchanged missing intake did not produce a stable plan")
	}
	after, err := os.ReadDir(dir)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("planning provisioned a namespace or ledger")
	}
	_, r := admittedIndexRecord(t, c, 0, "synthetic-empty-catalog")
	if c.runIndexRecord(c.projects[testProject], r) != nil {
		t.Fatal("real empty catalog fixture failed")
	}
	valid := requireRepairPlan(t, c, "not_needed")
	if valid.Files != 1 || valid.Bytes < 1 || valid.LedgerState != "ready" || valid.LedgerGeneration < 1 || valid.PlanID == missing.PlanID {
		t.Fatal("valid catalog plan lost its generation boundary")
	}
}

func TestProjectionRepairPlanCorruptCatalogPreservesCanonicalAndIntent(t *testing.T) {
	c, dir := repairPlanFixture(t)
	p := c.projects[testProject]
	created := createIssue(t, p.store, "synthetic-repair-canonical")
	req, r := admittedIndexRecord(t, c, 1, "synthetic-original-index-key")
	if c.runIndexRecord(p, r) != nil {
		t.Fatal("actual catalog publication failed")
	}
	old := requireRepairPlan(t, c, "not_needed")
	ledger := filepath.Join(dir, indexJobsDirectory, "state.json")
	before, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal("original index intent fixture unavailable")
	}
	input := []byte("synthetic-private-corrupt-body")
	if os.WriteFile(filepath.Join(dir, projectionDirectory, "catalog.json"), input, 0600) != nil {
		t.Fatal("corrupt catalog fixture unavailable")
	}
	plan := requireRepairPlan(t, c, "ready")
	if plan.Action != "quarantine" || !plan.RestartRequired || plan.Files != 1 || plan.Bytes != int64(len(input)) || plan.PlanID == old.PlanID {
		t.Fatal("corrupt catalog plan lost its precise effect boundary")
	}
	if plan != requireRepairPlan(t, c, "ready") {
		t.Fatal("unchanged corrupt metadata did not preserve plan identity")
	}
	body, _ := json.Marshal(plan)
	for _, forbidden := range []string{string(input), req.IdempotencyKey, dir, testActor.ID} {
		if forbidden != "" && strings.Contains(string(body), forbidden) {
			t.Fatal("repair view reflected protected input")
		}
	}
	after, err := os.ReadFile(ledger)
	if err != nil || string(after) != string(before) {
		t.Fatal("plan changed durable caller-key or publication history")
	}
	replay, err := c.EnqueueIndex(context.Background(), req, testActor)
	if err != nil || replay.State != "succeeded" || replay.Attempts != 1 {
		t.Fatal("corrupt catalog or planning replaced original successful intent")
	}
	read, err := c.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, Operation: "get", EntityID: created.Citation.EntityID})
	if err != nil || len(read.Entities) != 1 || read.Entities[0].Citation != created.Citation {
		t.Fatal("repair planning changed canonical source citation")
	}
	status, err := c.Status(context.Background(), testProject)
	if err != nil || status.AuthorityGeneration != created.ProjectSequence || status.Readiness != "ready" {
		t.Fatal("repair planning changed canonical source generation")
	}
	lexical, err := c.LexicalSearch(context.Background(), LexicalRequest{ProjectID: testProject, Query: "synthetic task", Limit: 8})
	if err != nil || len(lexical.Hits) != 1 || lexical.Hits[0].Citation != created.Citation {
		t.Fatal("damaged projection or planning disabled canonical lexical retrieval")
	}
}

func TestProjectionRepairPlanKeepsMissingFinalAndCancelledIntent(t *testing.T) {
	c, dir := repairPlanFixture(t)
	req, r := admittedIndexRecord(t, c, 0, "synthetic-cancelled-index-key")
	v, err := c.CancelIndex(context.Background(), IndexJobAction{ProjectID: testProject, JobID: r.Selection.BuildID, ExpectedGeneration: 1, WriteMode: "local"}, testActor)
	if err != nil || v.State != "cancelled" {
		t.Fatal("cancelled intent fixture failed")
	}
	ns := filepath.Join(dir, projectionDirectory)
	if os.Mkdir(ns, 0700) != nil {
		t.Fatal("orphan namespace fixture failed")
	}
	id, err := authority.NewUUID()
	if err != nil {
		t.Fatal("orphan metadata identity unavailable")
	}
	stage := filepath.Join(ns, "stage-"+string(id)+".json")
	if os.WriteFile(stage, []byte("{}"), 0600) != nil {
		t.Fatal("orphan stage fixture failed")
	}
	plan := requireRepairPlan(t, c, "ready")
	if plan.Files != 1 || plan.Bytes != 2 {
		t.Fatal("missing final was adopted or silently removed")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatal("planning removed orphan evidence")
	}
	replay, err := c.EnqueueIndex(context.Background(), req, testActor)
	if err != nil || replay != v {
		t.Fatal("planning revived cancelled durable intent")
	}
}

func TestProjectionRepairPlanBlocksActualPendingAndUnjoinedLifetimes(t *testing.T) {
	c, _ := repairPlanFixture(t)
	_, r := admittedIndexRecord(t, c, 0, "synthetic-pending-index-key")
	blocked := requireRepairPlan(t, c, "blocked")
	if blocked.Blocker != "index_active" || blocked.Action != "none" || blocked.RestartRequired {
		t.Fatal("pending intent granted a reset effect")
	}
	if _, err := c.CancelIndex(context.Background(), IndexJobAction{ProjectID: testProject, JobID: r.Selection.BuildID, ExpectedGeneration: 1, WriteMode: "local"}, testActor); err != nil {
		t.Fatal("pending fixture cancellation failed")
	}
	clear := requireRepairPlan(t, c, "not_needed")
	if clear.PlanID == blocked.PlanID || clear.LedgerGeneration <= blocked.LedgerGeneration {
		t.Fatal("durable cancellation did not invalidate stale plan")
	}
	p := c.projects[testProject]
	c.mu.Lock()
	p.activeIndex = r.Selection.BuildID
	c.mu.Unlock()
	requireRepairPlan(t, c, "blocked")
	c.mu.Lock()
	p.activeIndex = ""
	c.mu.Unlock()
	p.indexJobs.terminalUncertain.Store(true)
	_, err := c.PlanProjectionRepair(context.Background(), testProject)
	requireProjectionCode(t, err, IndexJobsUncertain)
}

func TestProjectionRepairPlanPhysicalAndContentFencesExcludeTimestamps(t *testing.T) {
	c, dir := repairPlanFixture(t)
	_, r := admittedIndexRecord(t, c, 0, "synthetic-stable-catalog-key")
	if c.runIndexRecord(c.projects[testProject], r) != nil {
		t.Fatal("stable catalog fixture failed")
	}
	file := filepath.Join(dir, projectionDirectory, "catalog.json")
	if os.WriteFile(file, []byte("synthetic-corrupt-catalog"), 0600) != nil {
		t.Fatal("corrupt content identity fixture failed")
	}
	before := requireRepairPlan(t, c, "ready")
	if os.Chtimes(file, time.Unix(100, 0), time.Unix(200, 0)) != nil {
		t.Fatal("timestamp-only fixture failed")
	}
	if before != requireRepairPlan(t, c, "ready") {
		t.Fatal("timestamp-only metadata changed content/physical plan identity")
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("same-content fixture unavailable")
	}
	replacement := filepath.Join(t.TempDir(), "synthetic-replacement")
	if os.WriteFile(replacement, body, 0600) != nil || os.Rename(replacement, file) != nil {
		t.Fatal("same-content inode fixture failed")
	}
	after := requireRepairPlan(t, c, "ready")
	if before.PlanID == after.PlanID {
		t.Fatal("same-content inode replacement retained stale plan authority")
	}
	createIssue(t, c.projects[testProject].store, "synthetic-new-source-generation")
	newSource := requireRepairPlan(t, c, "ready")
	if newSource.PlanID == after.PlanID || newSource.Sequence <= after.Sequence {
		t.Fatal("canonical generation change retained a stale plan")
	}
}

func TestProjectionRepairPlanRejectsUnsafeObjectsWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "unknown", "oversize", "mode", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			c, dir := repairPlanFixture(t)
			ns := filepath.Join(dir, projectionDirectory)
			if os.Mkdir(ns, 0700) != nil {
				t.Fatal("unsafe namespace fixture failed")
			}
			file := filepath.Join(ns, "catalog.json")
			switch kind {
			case "symlink", "hardlink":
				target := filepath.Join(t.TempDir(), "synthetic-owned-target")
				if os.WriteFile(target, []byte("{}"), 0600) != nil {
					t.Fatal("link target fixture failed")
				}
				var err error
				if kind == "symlink" {
					err = os.Symlink(target, file)
				} else {
					err = os.Link(target, file)
				}
				if err != nil {
					t.Fatal("unsafe link fixture failed")
				}
			case "fifo":
				if unix.Mkfifo(file, 0600) != nil {
					t.Fatal("FIFO fixture unavailable")
				}
			case "unknown":
				if os.WriteFile(filepath.Join(ns, "unknown-object"), []byte("{}"), 0600) != nil {
					t.Fatal("unknown object fixture failed")
				}
			case "oversize":
				f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal("oversize fixture failed")
				}
				err = f.Truncate(MaxProjectionBytes + 1)
				_ = f.Close()
				if err != nil {
					t.Fatal("oversize fixture failed")
				}
			case "mode":
				if os.WriteFile(file, []byte("{}"), 0600) != nil || os.Chmod(file, 0644) != nil {
					t.Fatal("unsafe mode fixture failed")
				}
			case "overflow":
				for i := 0; i < MaxProjectionStages+2; i++ {
					id, err := authority.NewUUID()
					if err != nil || os.WriteFile(filepath.Join(ns, "stage-"+string(id)+".json"), []byte("{}"), 0600) != nil {
						t.Fatal("overflow fixture failed")
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := c.PlanProjectionRepair(ctx, testProject)
			want := ProjectionCorrupt
			if kind == "overflow" {
				want = Capacity
			}
			requireProjectionCode(t, err, want)
			if ctx.Err() != nil {
				t.Fatal("unsafe object blocked beyond finite plan budget")
			}
			if _, err := os.Lstat(ns); err != nil {
				t.Fatal("unsafe plan modified original namespace")
			}
		})
	}
}

func TestProjectionRepairPlanContextAndContentionNeverAuthorize(t *testing.T) {
	c, _ := repairPlanFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.PlanProjectionRepair(ctx, testProject); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled plan ignored original deadline")
	}
	p := c.projects[testProject]
	p.turn.Lock()
	_, err := c.PlanProjectionRepair(context.Background(), testProject)
	p.turn.Unlock()
	if !authorityConflict(err) {
		t.Fatal("contended source turn granted plan authority")
	}
	p.indexJobs.mu.Lock()
	_, err = c.PlanProjectionRepair(context.Background(), testProject)
	p.indexJobs.mu.Unlock()
	if !authorityConflict(err) {
		t.Fatal("contended durable intent granted plan authority")
	}
	p.projection.mu.Lock()
	_, err = c.PlanProjectionRepair(context.Background(), testProject)
	p.projection.mu.Unlock()
	if !authorityConflict(err) {
		t.Fatal("contended catalog owner granted plan authority")
	}
}

func TestProjectionRepairPlanValidBytesDoNotClearOwnerFence(t *testing.T) {
	c, prepared, dir := deferredIndexCoordinator(t)
	_, r := admittedIndexRecord(t, c, 0, "synthetic-replaced-catalog-key")
	if c.runIndexRecord(c.projects[testProject], r) != nil {
		t.Fatal("valid owner fence fixture failed")
	}
	file := filepath.Join(dir, projectionDirectory, "catalog.json")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("valid replacement fixture unavailable")
	}
	replacement := filepath.Join(t.TempDir(), "synthetic-valid-catalog")
	if os.WriteFile(replacement, body, 0600) != nil || os.Rename(replacement, file) != nil {
		t.Fatal("valid replacement fixture failed")
	}
	_, err = c.PlanProjectionRepair(context.Background(), testProject)
	requireProjectionCode(t, err, ProjectionUncertain)
	if c.Close(context.Background()) != nil {
		t.Fatal("changed owner fixture did not join")
	}
	next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("qualified valid catalog reopen failed")
	}
	next.jobsStarted = true
	defer next.Close(context.Background())
	requireRepairPlan(t, next, "not_needed")
}

func TestProjectionRepairPlanFailedOwnerAdmissionCannotBecomeNotNeeded(t *testing.T) {
	for _, change := range []string{"restore_valid", "remove_namespace"} {
		t.Run(change, func(t *testing.T) {
			c, prepared, dir := deferredIndexCoordinator(t)
			created := createIssue(t, c.projects[testProject].store, "synthetic-failed-owner-source")
			_, r := admittedIndexRecord(t, c, created.ProjectSequence, "synthetic-failed-owner-index")
			if c.runIndexRecord(c.projects[testProject], r) != nil {
				t.Fatal("failed owner fixture publication unavailable")
			}
			file := filepath.Join(dir, projectionDirectory, "catalog.json")
			valid, err := os.ReadFile(file)
			if err != nil || c.Close(context.Background()) != nil {
				t.Fatal("failed owner fixture did not join")
			}
			ledger := filepath.Join(dir, indexJobsDirectory, "state.json")
			original, err := os.ReadFile(ledger)
			if err != nil {
				t.Fatal("failed owner intent bytes unavailable")
			}
			if os.WriteFile(file, []byte("synthetic-corrupt-final"), 0600) != nil {
				t.Fatal("failed owner corruption fixture unavailable")
			}
			next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
			if err != nil {
				t.Fatal("canonical authority was incorrectly fenced by derived corruption")
			}
			next.jobsStarted = true
			if next.projects[testProject].projection != nil {
				t.Fatal("corrupt final admitted a projection owner")
			}
			requireRepairPlan(t, next, "ready")
			if change == "restore_valid" {
				if os.WriteFile(file, valid, 0600) != nil {
					t.Fatal("valid replacement fixture failed")
				}
			} else {
				if os.Rename(filepath.Join(dir, projectionDirectory), filepath.Join(dir, "synthetic-preserved-metadata")) != nil {
					t.Fatal("preserved namespace fixture failed")
				}
			}
			_, err = next.PlanProjectionRepair(context.Background(), testProject)
			requireProjectionCode(t, err, ProjectionUncertain)
			status, err := next.Status(context.Background(), testProject)
			if err != nil || status.Readiness != "ready" || status.AuthorityGeneration != created.ProjectSequence {
				t.Fatal("failed projection admission hid unchanged canonical authority")
			}
			read, err := next.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, Operation: "get", EntityID: created.Citation.EntityID})
			if err != nil || len(read.Entities) != 1 || read.Entities[0].Citation != created.Citation {
				t.Fatal("failed admission repair plan changed original canonical citation")
			}
			after, err := os.ReadFile(ledger)
			if err != nil || string(after) != string(original) {
				t.Fatal("failed admission repair plan rewrote caller/publication history")
			}
			v, err := next.IndexJob(context.Background(), testProject, r.Selection.BuildID)
			if err != nil || v.State != "succeeded" || v.Attempts != 1 {
				t.Fatal("failed owner plan lost retained publication history")
			}
			if next.Close(context.Background()) != nil {
				t.Fatal("failed owner did not join")
			}
			final, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
			if err != nil {
				t.Fatal("explicit qualified owner reopen failed")
			}
			final.jobsStarted = true
			defer final.Close(context.Background())
			requireRepairPlan(t, final, "not_needed")
		})
	}
}

func TestProjectionRepairPlanRetainsUnknownPublicationAcrossReopen(t *testing.T) {
	c, prepared, dir := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	_, r := admittedIndexRecord(t, c, 0, "synthetic-uncertain-index-key")
	started, err := p.indexJobs.transition(context.Background(), r.Selection.BuildID, 1, func(next *indexJobRecord) { next.State = "running"; next.Attempts++ }, nil)
	if err != nil {
		t.Fatal("uncertain attempt fixture failed")
	}
	_, err = p.indexJobs.transition(context.Background(), r.Selection.BuildID, started.Generation, func(next *indexJobRecord) { next.State, next.Code = "interrupted", "outcome_uncertain" }, nil)
	if err != nil {
		t.Fatal("uncertain terminal fixture failed")
	}
	plan := requireRepairPlan(t, c, "blocked")
	if plan.Blocker != "index_uncertain" {
		t.Fatal("unknown publication was treated as inactive disposable history")
	}
	if c.Close(context.Background()) != nil {
		t.Fatal("uncertain fixture did not join")
	}
	next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("uncertain owner reopen failed")
	}
	next.jobsStarted = true
	defer next.Close(context.Background())
	after := requireRepairPlan(t, next, "blocked")
	if after != plan {
		t.Fatal("verified reopen silently cleared uncertain publication intent")
	}
}

func TestProjectionRepairPlanViewRejectsImpossibleEffects(t *testing.T) {
	c, _ := repairPlanFixture(t)
	base := requireRepairPlan(t, c, "not_needed")
	controls := []func(*ProjectionRepairPlan){
		func(p *ProjectionRepairPlan) { p.Schema = 0 },
		func(p *ProjectionRepairPlan) { p.ProjectID = "" },
		func(p *ProjectionRepairPlan) { p.PlanID = "synthetic-private-peer-body" },
		func(p *ProjectionRepairPlan) { p.Action = "purge" },
		func(p *ProjectionRepairPlan) { p.State = "unknown" },
		func(p *ProjectionRepairPlan) { p.Bytes = 1 },
		func(p *ProjectionRepairPlan) { p.Files = 1; p.Bytes = MaxProjectionBytes + 1 },
		func(p *ProjectionRepairPlan) { p.RestartRequired = true },
		func(p *ProjectionRepairPlan) { p.LedgerState = "ready" },
		func(p *ProjectionRepairPlan) { p.LedgerGeneration = 1 },
		func(p *ProjectionRepairPlan) { p.State = "blocked"; p.Blocker = "index_active" },
		func(p *ProjectionRepairPlan) { p.State = "ready"; p.Action = "quarantine" },
	}
	for _, change := range controls {
		p := base
		change(&p)
		if p.Validate() == nil {
			t.Fatal("repair view granted an inconsistent/unknown effect")
		}
	}
}
