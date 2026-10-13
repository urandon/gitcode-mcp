package localauthority

import (
	"context"
	"errors"
	"sort"

	"gitcode-mcp/internal/authority"
)

func recoveryEffectCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var e Error
	if errors.As(err, &e) && recoveryJobCode(string(e.Code)) {
		return string(e.Code)
	}
	var a authority.Error
	if errors.As(err, &a) && recoveryJobCode(string(a.Code)) {
		return string(a.Code)
	}
	return "operation_failed"
}

func authorityConflict(err error) bool {
	var e authority.Error
	return errors.As(err, &e) && e.Code == authority.Conflict
}

type recoveryWait struct{ changed <-chan struct{} }

func (recoveryWait) Error() string { return "local recovery waits for an owned turn" }
func (recoveryWait) Unwrap() error { return authority.Error{Code: authority.Conflict} }

type recoveryGenerationChanged struct{}

func (recoveryGenerationChanged) Error() string { return "local recovery attempt generation changed" }
func (recoveryGenerationChanged) Unwrap() error { return authority.Error{Code: authority.Conflict} }

func finishRecoveryMetadata(ctx context.Context, q *recoveryQueue, r recoveryJobRecord, state, code string, receipt *authority.MutationReceipt, reconciled bool) (RecoveryJobView, error) {
	for {
		changed := q.mu.changed()
		view, err := q.finish(ctx, r.View.JobID, r.View.Generation, state, code, receipt, reconciled)
		var superseded recoveryGenerationChanged
		if !authorityConflict(err) || errors.As(err, &superseded) {
			return view, err
		}
		select {
		case <-ctx.Done():
			return RecoveryJobView{}, ctx.Err()
		case <-changed:
		}
	}
}

func retainedRecoveryMetadata(ctx context.Context, q *recoveryQueue, id authority.UUID) (RecoveryJobView, bool, error) {
	for {
		changed := q.mu.changed()
		view, found, err := q.retained(ctx, id)
		if !authorityConflict(err) {
			return view, found, err
		}
		select {
		case <-ctx.Done():
			return RecoveryJobView{}, false, ctx.Err()
		case <-changed:
		}
	}
}

func (c *Coordinator) recoveryWorker(p *coordinatedProject) {
	defer c.jobWorkers.Done()
	q := p.jobs
	for {
		select {
		case <-c.lifetime.Done():
			return
		case <-q.wake:
		}
		for {
			if c.lifetime.Err() != nil {
				return
			}
			metadataChanged := q.mu.changed()
			records, err := q.records(c.lifetime)
			if authorityConflict(err) {
				select {
				case <-c.lifetime.Done():
					return
				case <-metadataChanged:
					continue
				}
			}
			if err != nil {
				break
			}
			sort.SliceStable(records, func(i, j int) bool {
				priority := func(r recoveryJobRecord) int {
					switch r.View.State {
					case "queued", "running":
						return 0
					case "cancelling":
						return 1
					case "succeeded":
						return 3
					default:
						return 2
					}
				}
				a, b := priority(records[i]), priority(records[j])
				if a != b {
					return a < b
				}
				return records[i].AdmittedGeneration < records[j].AdmittedGeneration
			})
			blocked := false
			for _, r := range records {
				if c.lifetime.Err() != nil {
					return
				}
				if r.View.State == "succeeded" {
					continue
				}
				err = c.runRecoveryRecord(p, r)
				if authorityConflict(err) {
					var busy recoveryWait
					if !errors.As(err, &busy) {
						blocked = true
						break
					}
					// Existing durable work waits for real lock release. No busy
					// loop, provider amplification or fabricated actual attempt.
					select {
					case <-c.lifetime.Done():
						return
					case <-busy.changed:
					case <-q.wake:
					}
					blocked = true
					break
				}
				if err != nil {
					break
				}
			}
			if blocked {
				continue
			}
			break
		}
	}
}

func (c *Coordinator) runRecoveryRecord(p *coordinatedProject, selected recoveryJobRecord) error {
	q := p.jobs
	s, call, release, changed, err := c.beginWithWait(c.lifetime, selected.View.ProjectID)
	if err != nil {
		if authorityConflict(err) {
			return recoveryWait{changed}
		}
		return err
	}
	defer release()
	metadataChanged := q.mu.changed()
	current, found, err := q.retained(call, selected.View.JobID)
	if err != nil {
		if authorityConflict(err) {
			return recoveryWait{metadataChanged}
		}
		return err
	}
	if !found {
		return nil
	}
	if current.State == "succeeded" {
		return nil
	}
	metadataChanged = q.mu.changed()
	records, err := q.records(call)
	if err != nil {
		if authorityConflict(err) {
			return recoveryWait{metadataChanged}
		}
		return err
	}
	i, found := recoveryRecordIndex(records, current.JobID)
	if !found {
		return nil
	}
	r := records[i]
	o, err := s.inspectRecoveryOperation(call, r.View.ProjectID, r.View.JobID, r.View.ObservedJournalGeneration)
	if err != nil {
		if call.Err() != nil {
			// Shutdown preserves durable authorization for the next owner.
			// A live-owner pre-effect deadline instead records an explicit
			// interrupted outcome, so Retry can admit a new bounded attempt.
			if c.lifetime.Err() != nil || r.View.State != "queued" && r.View.State != "running" && r.View.State != "cancelling" {
				return call.Err()
			}
			proof, cancel := context.WithTimeout(context.WithoutCancel(call), CoordinatorCallBudget)
			defer cancel()
			_, e := finishRecoveryMetadata(proof, q, r, "interrupted", recoveryEffectCode(call.Err()), nil, false)
			return e
		}
		var missing authority.Error
		if errors.As(err, &missing) && missing.Code == authority.NoAuthority {
			q.mu.Lock()
			q.fence = JobsCorrupt
			q.unlock()
			return Error{JobsCorrupt}
		}
		if r.View.State == "queued" || r.View.State == "running" || r.View.State == "cancelling" {
			proof, cancel := context.WithTimeout(context.WithoutCancel(call), CoordinatorCallBudget)
			defer cancel()
			_, e := finishRecoveryMetadata(proof, q, r, "interrupted", "outcome_uncertain", nil, false)
			return e
		}
		return nil
	}
	if o.Intent != r.IntentHash {
		q.mu.Lock()
		q.fence = JobsCorrupt
		q.unlock()
		return Error{JobsCorrupt}
	}
	if o.Phase == "settled" {
		return c.reconcileRecoveryOutcome(s, q, r, call, nil, true)
	}
	if r.View.State == "cancelling" {
		// Old runtime's worker is gone before this owner could open the
		// global UUID lease. Never resume a cancelled source effect.
		_, err = finishRecoveryMetadata(call, q, r, "cancelled", "cancelled", nil, false)
		return err
	}
	if r.View.State != "queued" && r.View.State != "running" {
		return nil
	}
	if r.View.Attempts >= MaxRecoveryJobAttempts {
		_, err = finishRecoveryMetadata(call, q, r, "interrupted", string(JobRetryLimit), nil, false)
		return err
	}
	metadataChanged = q.mu.changed()
	r, err = q.startAttempt(call, r.View.JobID)
	if err != nil {
		if authorityConflict(err) {
			return recoveryWait{metadataChanged}
		}
		return err
	}
	effect, stop := context.WithCancel(call)
	registered := c.setRecoveryAttempt(p, r.View.JobID, r.View.Generation, stop)
	if !registered {
		stop()
	}
	view, exists, e := q.retained(call, r.View.JobID)
	if e != nil || !exists || view.State != "running" || view.Generation != r.View.Generation {
		stop()
	}
	_, effectErr := s.recoverSelectedDocument(effect, r.selected())
	stop()
	c.clearRecoveryAttempt(p, r.View.JobID, r.View.Generation)
	return c.reconcileRecoveryOutcome(s, q, r, call, effectErr, false)
}

// Every effect error is classified from fresh canonical phase/durability,
// never from its text. This runs AFTER the actual source call has joined while
// retaining its project turn. Shutdown cannot release the writer underneath it.
func (c *Coordinator) reconcileRecoveryOutcome(s *Store, q *recoveryQueue, r recoveryJobRecord, call context.Context, effectErr error, proofOnly bool) error {
	proof, cancel := context.WithTimeout(context.WithoutCancel(call), CoordinatorCallBudget)
	defer cancel()
	o, err := s.inspectRecoveryOperation(proof, r.View.ProjectID, r.View.JobID, r.View.ObservedJournalGeneration)
	if err == nil && o.Intent != r.IntentHash {
		q.mu.Lock()
		q.fence = JobsCorrupt
		q.unlock()
		return Error{JobsCorrupt}
	}
	if err == nil && o.Phase == "settled" {
		result, e := s.recoverSelectedDocument(proof, r.selected())
		if e == nil {
			_, e = finishRecoveryMetadata(proof, q, r, "succeeded", "", &result.Receipt, proofOnly || effectErr != nil)
			return e
		}
		err = e
	}
	view, found, e := retainedRecoveryMetadata(proof, q, r.View.JobID)
	if e != nil {
		return e
	}
	if !found {
		return Error{JobNotFound}
	}
	if proofOnly && view.State != "queued" && view.State != "running" && view.State != "cancelling" {
		return nil
	}
	state, code := "interrupted", "outcome_uncertain"
	if err == nil && o.Phase != "settled" {
		if view.State == "cancelling" {
			state, code = "cancelled", "cancelled"
		} else if c.lifetime.Err() == nil {
			state, code = "failed", recoveryEffectCode(effectErr)
		} else {
			code = "interrupted"
		}
	}
	_, err = finishRecoveryMetadata(proof, q, r, state, code, nil, false)
	return err
}
