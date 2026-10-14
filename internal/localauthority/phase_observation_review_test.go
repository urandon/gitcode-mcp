//go:build darwin || linux

package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type reviewCancelAtCapture struct {
	context.Context
	cancel context.CancelFunc
	hit    bool
}

func (c *reviewCancelAtCapture) Value(key any) any {
	value := c.Context.Value(key)
	if _, ok := key.(phaseObservationKey); ok && !c.hit {
		c.hit = true
		c.cancel()
	}
	return value
}

func TestReviewPhaseRenameCancellationCannotDisappearFromCoverage(t *testing.T) {
	c, _, _, collector := observedProjectFixture(t)
	resume, err := collector.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("observation fixture unavailable")
	}
	defer resume()
	p := c.projects[testProject]
	createIssue(t, p.store, "synthetic-review-phase-source")
	_, record := admittedIndexRecord(t, c, 1, "synthetic-review-phase-index")
	_, call, release, err := c.begin(context.Background(), testProject)
	if err != nil {
		t.Fatal("source turn unavailable")
	}
	defer release()
	ctx, cancel := context.WithCancel(call)
	defer cancel()
	cut := &reviewCancelAtCapture{Context: ctx, cancel: cancel}
	_, err = p.indexJobs.transition(cut, record.Selection.BuildID, record.Generation, func(next *indexJobRecord) {
		next.State = "running"
		next.Attempts++
	}, nil)
	if !cut.hit || !errors.Is(err, context.Canceled) {
		t.Fatal("actual publication cancellation checkpoint not reached")
	}
	body, exists, _, err := p.indexJobs.readFile("state.json")
	var persisted indexJobEnvelope
	if err != nil {
		t.Fatal("retained file unreadable")
	}
	if !exists {
		t.Fatal("retained file missing")
	}
	if json.Unmarshal(body, &persisted) != nil {
		t.Fatal("retained file invalid JSON")
	}
	if len(persisted.State.Jobs) != 1 {
		t.Fatal("retained file cardinality mismatch")
	}
	if persisted.State.Jobs[0].State != "running" {
		t.Fatal("actual final rename was not retained")
	}
	release()
	page := phaseEvidence(t, c, collector, resume)
	if len(page.Events) != 1 || page.Events[0].Outcome != "queued" {
		t.Fatal("unexpected admitted phase control evidence")
	}
	if page.Coverage.Dropped != 1 {
		t.Fatal("actual uncertain rename disappeared from observation coverage")
	}
}
