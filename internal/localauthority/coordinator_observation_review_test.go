//go:build darwin || linux

package localauthority

import (
	"context"
	"errors"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestReviewKnownPreCommitCancellationDoesNotInventBoundaryLoss(t *testing.T) {
	c, _, _, collector := observedProjectFixture(t)
	resume, err := collector.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("collector idle boundary unavailable")
	}
	defer resume()
	before, err := c.Status(context.Background(), testProject)
	if err != nil {
		t.Fatal("source status unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.projects[testProject].store.fault = func(phase string) error {
		if phase == "generation" {
			cancel()
		}
		return nil
	}
	req := request(t, "issue.create", "synthetic-cancelled-before-commit", authority.IssuePayload{Title: "Synthetic cancellation control", State: "open"})
	_, err = c.Mutate(ctx, req, testActor)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("caller cancellation was not preserved")
	}
	c.projects[testProject].store.fault = nil
	after, err := c.Status(context.Background(), testProject)
	if err != nil || after.AuthorityGeneration != before.AuthorityGeneration {
		t.Fatal("cancelled transaction changed canonical sequence")
	}
	var count int
	if err := c.projects[testProject].store.db.QueryRowContext(context.Background(), "SELECT count(*) FROM receipts").Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled transaction retained a receipt")
	}
	if collector.Coverage().Dropped != 0 {
		t.Fatal("known pre-driver rollback invented unconfirmed publication loss")
	}
}

func TestDocumentSettlementKnownPreDriverCancellationHasNoObservation(t *testing.T) {
	c, _, _, collector := observedProjectFixture(t)
	resume, err := collector.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("document observation idle boundary unavailable")
	}
	defer resume()
	s := c.projects[testProject].store
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	capture := &canonicalObservation{project: testProject, document: true}
	ctx = context.WithValue(ctx, canonicalObservationKey{}, capture)
	// Exercise the actual SQL transaction and settlement helper, not a fake
	// Commit response. Cancellation is known before the driver boundary.
	s.mu.Lock()
	var before uint64
	if s.db.QueryRowContext(ctx, "SELECT generation FROM project WHERE singleton=1").Scan(&before) != nil {
		s.mu.Unlock()
		t.Fatal("document control generation unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		t.Fatal("document control transaction unavailable")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE project SET generation=generation+1 WHERE singleton=1"); err != nil {
		s.mu.Unlock()
		t.Fatal("document control transaction did not stage")
	}
	cancel()
	err = s.commitKnowledge(ctx, tx, "knowledge_document_settled")
	var after uint64
	readErr := s.db.QueryRowContext(context.Background(), "SELECT generation FROM project WHERE singleton=1").Scan(&after)
	s.mu.Unlock()
	capture.publish(c.projects[testProject].observation)
	if !errors.Is(err, context.Canceled) || readErr != nil || after != before {
		t.Fatal("cancelled document transaction changed canonical state or outcome")
	}
	if capture.state != 0 || collector.Coverage().Dropped != 0 {
		t.Fatal("known document rollback invented a publication boundary")
	}
}
