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
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func currentHealth(t *testing.T, c *Coordinator) RuntimeHealthView {
	t.Helper()
	req := RuntimeHealthRequest{testProject, c.RuntimeBinding()}
	v, err := c.RuntimeHealth(context.Background(), req)
	if err != nil || VerifyRuntimeHealth(req, v) != nil {
		t.Fatal("current owner health did not return its exact qualified view")
	}
	return v
}

func TestRuntimeHealthMissingReadDoesNotProvision(t *testing.T) {
	c, _, dir := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal("health fixture inventory unavailable")
	}
	p.store.fault = func(string) error { t.Fatal("health invoked source publication"); return nil }
	v := currentHealth(t, c)
	p.store.fault = nil
	if v.Phase != "starting" || v.Tracker.State != "ready" || v.Tracker.SourceSequence == nil || *v.Tracker.SourceSequence != 0 || v.Projection.State != "missing" || v.Projection.IndexedSequence != nil || v.IndexJobs.State != "missing" || v.Repair != "missing" || v.RecoveryJobs.State != "ready" || v.Knowledge.State != "uninitialized" {
		t.Fatal("missing metadata became coverage or hid the empty qualified source")
	}
	after, err := os.ReadDir(dir)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("health provisioned a namespace")
	}
	lease, err := c.AdmitServing(context.Background())
	if err != nil || currentHealth(t, c).Phase != "serving" {
		t.Fatal("validated serving boundary was not retained")
	}
	lease.Close()
	if currentHealth(t, c).Phase != "not_serving" {
		t.Fatal("ended serving lease remained current")
	}
	body, _ := json.Marshal(v)
	if len(body) > MaxRuntimeHealthBytes || strings.Contains(string(body), dir) || strings.Contains(string(body), testActor.ID) {
		t.Fatal("health exposed private coordinates or exceeded its cap")
	}
}

func TestRuntimeHealthServingLeaseRetainsAdmissionUntilHandlerJoin(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	old, err := c.AdmitServing(context.Background())
	if err != nil {
		t.Fatal("serving lease unavailable")
	}
	old.Seal()
	if currentHealth(t, c).Phase != "not_serving" {
		t.Fatal("sealed listener still proved serving")
	}
	if next, err := c.AdmitServing(context.Background()); err == nil {
		next.Close()
		t.Fatal("replacement server admitted before old handler join")
	}
	old.Close() // fixture owns no handlers; this is its joined boundary
	next, err := c.AdmitServing(context.Background())
	if err != nil {
		t.Fatal("joined listener lease blocked explicit replacement")
	}
	defer next.Close()
	old.Close()
	if currentHealth(t, c).Phase != "serving" {
		t.Fatal("old lease closure cleared a replacement")
	}
}

func TestRuntimeHealthTransientWriterReadOnlyDoesNotInventRecovery(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	s := c.projects[testProject].store
	if _, err := s.db.ExecContext(context.Background(), "PRAGMA synchronous=OFF"); err != nil {
		t.Fatal("actual writer durability control unavailable")
	}
	v := currentHealth(t, c)
	if v.Tracker.State != "read_only" || v.Tracker.Writer != "unavailable" || v.Tracker.SourceSequence == nil || *v.Tracker.SourceSequence != 0 || s.fenced || s.logicalFenced {
		t.Fatal("transient writer refusal lost canonical readiness or invented a durable fence")
	}
	if _, err := s.db.ExecContext(context.Background(), "PRAGMA synchronous=FULL"); err != nil {
		t.Fatal("actual writer durability restore unavailable")
	}
	v = currentHealth(t, c)
	if v.Tracker.State != "ready" || v.Tracker.Writer != "owned" || s.fenced || s.logicalFenced {
		t.Fatal("read qualification poisoned a restored writer")
	}
}

func TestRuntimeHealthCatalogSequenceIsNotPublicationGeneration(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	_, first := admittedIndexRecord(t, c, 0, "synthetic-health-empty")
	if c.runIndexRecord(p, first) != nil {
		t.Fatal("actual zero-sequence catalog unavailable")
	}
	v := currentHealth(t, c)
	if v.Projection.State != "current" || v.Projection.IndexedSequence == nil || *v.Projection.IndexedSequence != 0 || p.projection.catalog.Generation == 0 {
		t.Fatal("qualified zero-sequence catalog confused with absence or publication generation")
	}
	createIssue(t, p.store, "synthetic-health-source")
	v = currentHealth(t, c)
	if v.Projection.State != "stale" || *v.Projection.IndexedSequence != 0 || *v.Tracker.SourceSequence != 1 {
		t.Fatal("source mutation did not expose catalog staleness")
	}
	_, next := admittedIndexRecord(t, c, 1, "synthetic-health-updated")
	if c.runIndexRecord(p, next) != nil {
		t.Fatal("actual updated catalog unavailable")
	}
	v = currentHealth(t, c)
	if v.Projection.State != "current" || *v.Projection.IndexedSequence != 1 || v.IndexJobs.Counts == nil || v.IndexJobs.Counts.Succeeded != 2 {
		t.Fatal("health lost current catalog or exact retained job counts")
	}
	p.indexJobs.terminalUncertain.Store(true)
	v = currentHealth(t, c)
	if v.IndexJobs.State != "uncertain" || v.Projection.State != "uncertain" || v.Projection.IndexedSequence != nil {
		t.Fatal("valid files cleared current-owner uncertainty")
	}
}

func TestRuntimeHealthDerivedDamageDoesNotDisableCanonicalReads(t *testing.T) {
	c, _, dir := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	created := createIssue(t, p.store, "synthetic-health-canonical")
	req, r := admittedIndexRecord(t, c, 1, "synthetic-health-retained-index")
	if c.runIndexRecord(p, r) != nil {
		t.Fatal("actual catalog unavailable")
	}
	ledger, err := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
	if err != nil {
		t.Fatal("actual retained ledger unavailable")
	}
	damage := []byte("synthetic-private-invalid-catalog")
	if os.WriteFile(filepath.Join(dir, projectionDirectory, "catalog.json"), damage, 0600) != nil {
		t.Fatal("damage fixture unavailable")
	}
	v := currentHealth(t, c)
	if v.Projection.State != "corrupt" || v.Tracker.State != "ready" || v.IndexJobs.State != "ready" {
		t.Fatal("derived damage collapsed unrelated canonical readiness")
	}
	body, _ := json.Marshal(v)
	if strings.Contains(string(body), string(damage)) || strings.Contains(string(body), req.IdempotencyKey) || strings.Contains(string(body), string(created.Citation.EntityID)) {
		t.Fatal("health exposed source, operation or raw error data")
	}
	read, err := c.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, Operation: "get", EntityID: created.Citation.EntityID})
	if err != nil || len(read.Entities) != 1 {
		t.Fatal("health inspection disabled intact canonical read")
	}
	lexical, err := c.LexicalSearch(context.Background(), LexicalRequest{ProjectID: testProject, Query: "synthetic task", Limit: 8})
	if err != nil || len(lexical.Hits) != 1 {
		t.Fatal("health inspection disabled lexical read")
	}
	after, err := os.ReadFile(filepath.Join(dir, indexJobsDirectory, "state.json"))
	if err != nil || string(after) != string(ledger) {
		t.Fatal("health rewrote retained index intent")
	}
}

func TestRuntimeHealthRefusesForeignEpochAndEveryContendedLock(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	req := RuntimeHealthRequest{testProject, c.RuntimeBinding()}
	foreign := req
	foreign.RuntimeBinding = strings.Repeat("a", 64)
	if _, err := c.RuntimeHealth(context.Background(), foreign); err == nil {
		t.Fatal("request installed a foreign owner epoch")
	}
	for _, m := range []*releaseMutex{&p.turn, &p.jobs.mu, &p.indexJobs.mu, &p.repair.mu, &p.projection.mu} {
		m.Lock()
		_, err := c.RuntimeHealth(context.Background(), req)
		m.Unlock()
		var typed authority.Error
		if !errors.As(err, &typed) || typed.Code != authority.Conflict {
			t.Fatal("health waited or fabricated readiness across component contention")
		}
	}
	p.store.mu.Lock()
	_, err := c.RuntimeHealth(context.Background(), req)
	p.store.mu.Unlock()
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != authority.Conflict {
		t.Fatal("health waited behind canonical store contention")
	}
	if c.Close(context.Background()) != nil {
		t.Fatal("health owner did not close")
	}
	if _, err := c.RuntimeHealth(context.Background(), req); err == nil {
		t.Fatal("closed owner returned current health")
	}
}

func TestRuntimeHealthCancellationKeepsCanonicalReadiness(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	readyKnowledgeSetupFixture(t, p.store, bindKnowledgeSetupFixture(t, p.store, reserveKnowledgeSetupFixture(t, p.store)))
	req := documentRequest(t, "document.create", "synthetic-health-cancel", authority.EmptyKnowledgeRef, authority.Citation{}, "# Actual health cancellation fixture\n")
	if _, err := c.MutateDocument(context.Background(), req, testActor); err != nil {
		t.Fatal("actual health knowledge fixture unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.store.knowledgeReadCheckpoint = func(at string) {
		if at == "knowledge_layout_object" {
			cancel()
		}
	}
	_, err := c.RuntimeHealth(ctx, RuntimeHealthRequest{testProject, c.RuntimeBinding()})
	p.store.knowledgeReadCheckpoint = nil
	if !errors.Is(err, context.Canceled) || p.store.fenced || p.store.logicalFenced {
		t.Fatal("current health interruption became corruption or fenced canonical state")
	}
	if currentHealth(t, c).Knowledge.State != "ready" {
		t.Fatal("interruption damaged current knowledge readiness")
	}
}

func TestRuntimeHealthActualSQLCorruptionCannotBecomeReady(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	s := c.projects[testProject].store
	createIssue(t, s, "synthetic-health-corruption")
	corruptExec(t, s.db, "UPDATE project SET generation=generation+1 WHERE singleton=1")
	v := currentHealth(t, c)
	if v.Tracker.State != "corrupt" || v.Tracker.SourceSequence != nil || v.Knowledge.State != "unavailable" || !s.fenced || !s.logicalFenced {
		t.Fatal("health hid logical corruption or retained an unqualified generation")
	}
	// An external edit back to valid bytes does not qualify the live owner.
	corruptExec(t, s.db, "UPDATE project SET generation=generation-1 WHERE singleton=1")
	v = currentHealth(t, c)
	if v.Tracker.State != "corrupt" || v.Tracker.SourceSequence != nil || !s.logicalFenced {
		t.Fatal("valid bytes cleared an existing logical fence")
	}
}

func TestRuntimeHealthPhysicalReplacementRefusesWithoutAdoption(t *testing.T) {
	c, _, dir := deferredIndexCoordinator(t)
	s := c.projects[testProject].store
	file := filepath.Join(dir, "tracker.db")
	body, err := os.ReadFile(file)
	if err != nil || os.Rename(file, file+".retained") != nil || os.WriteFile(file, body, 0600) != nil {
		t.Fatal("physical source replacement fixture unavailable")
	}
	_, err = c.RuntimeHealth(context.Background(), RuntimeHealthRequest{testProject, c.RuntimeBinding()})
	var typed Error
	if !errors.As(err, &typed) || typed.Code != Corrupt || !s.fenced {
		t.Fatal("health adopted a replacement source database")
	}
	after, err := os.ReadFile(file)
	if err != nil || string(after) != string(body) {
		t.Fatal("health modified the replacement source")
	}
}

func TestRuntimeHealthRepairRestartRequirementSurvivesValidFiles(t *testing.T) {
	c, _, _, req, _, _ := repairApplyFixture(t)
	if _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err != nil {
		t.Fatal("actual repair did not complete")
	}
	v := currentHealth(t, c)
	if v.Repair != "restart_required" || v.Projection.State != "restart_required" || v.Projection.IndexedSequence != nil || v.Tracker.State != "ready" {
		t.Fatal("health erased the actual repair owner restart fence")
	}
}

func TestRuntimeHealthReportsActualAttemptSeparatelyFromCatalog(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	_, r := admittedIndexRecord(t, c, 0, "synthetic-health-active-attempt")
	paused, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	p.indexTurnFault = func(at string) error {
		if at == "index_turn_complete" {
			once.Do(func() { close(paused); <-resume })
		}
		return nil
	}
	c.jobWorkers.Add(1) // fixture owns the actual admitted attempt's joined lifetime
	go func() { defer c.jobWorkers.Done(); done <- c.runIndexRecord(p, r) }()
	var release sync.Once
	join := func() { release.Do(func() { close(resume) }) }
	t.Cleanup(join)
	select {
	case <-paused:
	case <-time.After(time.Second):
		join()
		<-done
		t.Fatal("actual index did not reach released publication turn")
	}
	v := currentHealth(t, c)
	if v.Projection.State != "current" || !v.IndexJobs.WorkerActive || v.IndexJobs.Counts == nil || v.IndexJobs.Counts.Running != 1 {
		join()
		<-done
		t.Fatal("health inferred worker join from catalog publication")
	}
	join()
	if <-done != nil {
		t.Fatal("actual health attempt did not settle")
	}
	v = currentHealth(t, c)
	if v.IndexJobs.WorkerActive || v.IndexJobs.Counts == nil || v.IndexJobs.Counts.Succeeded != 1 {
		t.Fatal("joined actual attempt did not update independent job state")
	}
}
