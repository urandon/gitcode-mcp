package localauthority

import (
	"context"
	"errors"
	"sort"
	"time"

	"gitcode-mcp/internal/authority"
)

const indexAttemptBudget = 5 * time.Minute

type indexWait struct{ changed <-chan struct{} }

func (indexWait) Error() string { return "local index waits for an owned turn" }
func (indexWait) Unwrap() error { return authority.Error{Code: authority.Conflict} }

type indexGenerationChanged struct{}

func (indexGenerationChanged) Error() string { return "local index attempt generation changed" }
func (indexGenerationChanged) Unwrap() error { return authority.Error{Code: authority.Conflict} }

func waitIndexRetained(ctx context.Context, q *indexQueue, id authority.UUID) (indexJobRecord, bool, error) {
	for {
		changed := q.mu.changed()
		r, found, err := q.retained(ctx, id)
		if !authorityConflict(err) {
			return r, found, err
		}
		select {
		case <-ctx.Done():
			return indexJobRecord{}, false, ctx.Err()
		case <-changed:
		}
	}
}

func waitIndexTransition(ctx context.Context, q *indexQueue, id authority.UUID, generation uint64, change func(*indexJobRecord)) (indexJobRecord, error) {
	for {
		changed := q.mu.changed()
		r, err := q.transition(ctx, id, generation, change, nil)
		var stale indexGenerationChanged
		if !authorityConflict(err) || errors.As(err, &stale) {
			return r, err
		}
		select {
		case <-ctx.Done():
			return indexJobRecord{}, ctx.Err()
		case <-changed:
		}
	}
}

func waitIndexProof(ctx context.Context, p *projectionIndex, selected indexSelection) (indexPublication, bool, error) {
	for {
		changed := p.mu.changed()
		proof, found, err := p.indexProof(ctx, selected)
		if !authorityConflict(err) {
			return proof, found, err
		}
		select {
		case <-ctx.Done():
			return indexPublication{}, false, ctx.Err()
		case <-changed:
		}
	}
}

func (c *Coordinator) indexWorker(p *coordinatedProject) {
	defer c.jobWorkers.Done()
	q := p.indexJobs
	for {
		select {
		case <-c.lifetime.Done():
			return
		case <-q.wake:
		}
		for {
			changed := q.mu.changed()
			records, err := q.records(c.lifetime)
			if authorityConflict(err) {
				select {
				case <-c.lifetime.Done():
					return
				case <-changed:
					continue
				}
			}
			if err != nil {
				break
			}
			// Reconcile earlier exact catalog publication before a newer job
			// can advance it. UUID ordering or timestamps are not authority.
			sort.Slice(records, func(i, j int) bool { return records[i].AdmittedGeneration < records[j].AdmittedGeneration })
			waited := false
			for _, r := range records {
				if c.lifetime.Err() != nil {
					return
				}
				if r.State == "succeeded" {
					continue
				}
				err = c.runIndexRecord(p, r)
				var wait indexWait
				if errors.As(err, &wait) {
					select {
					case <-c.lifetime.Done():
						return
					case <-wait.changed:
					case <-q.wake:
					}
					waited = true
					break
				}
				if err != nil {
					break
				}
			}
			if waited {
				continue
			}
			break
		}
	}
}

func (c *Coordinator) runIndexRecord(p *coordinatedProject, selected indexJobRecord) error {
	_, call, release, changed, err := c.beginWithWait(c.lifetime, selected.Selection.ProjectID)
	if err != nil {
		if authorityConflict(err) {
			return indexWait{changed}
		}
		return err
	}
	q := p.indexJobs
	metadataChanged := q.mu.changed()
	r, found, err := q.retained(call, selected.Selection.BuildID)
	if err != nil || !found {
		release()
		if authorityConflict(err) {
			return indexWait{metadataChanged}
		}
		return err
	}
	projectionChanged := p.projection.mu.changed()
	proof, published, err := p.projection.indexProof(call, r.Selection)
	if err != nil {
		release()
		if authorityConflict(err) {
			return indexWait{projectionChanged}
		}
		return err
	}
	if published {
		err = finishIndexMetadata(call, q, r.Selection.BuildID, "succeeded", "", &proof)
		release()
		return err
	}
	if r.State == "cancelling" {
		err = finishIndexMetadata(call, q, r.Selection.BuildID, "cancelled", "cancelled", nil)
		release()
		return err
	}
	if r.State != "queued" && r.State != "running" {
		release()
		return nil
	}
	if r.Attempts >= maxIndexAttempts {
		err = finishIndexMetadata(call, q, r.Selection.BuildID, "interrupted", "interrupted", nil)
		release()
		return err
	}
	r, err = waitIndexTransition(call, q, r.Selection.BuildID, r.Generation, func(next *indexJobRecord) { next.State = "running"; next.Attempts++ })
	if err != nil {
		release()
		return err
	}
	effect, cancel := context.WithTimeout(c.lifetime, indexAttemptBudget)
	if !c.setIndexAttempt(p, r, cancel) {
		cancel()
	}
	defer func() { cancel(); c.clearIndexAttempt(p, r) }()
	intake, stop := context.WithCancel(call)
	stopOnEffect := context.AfterFunc(effect, stop)
	b, err := waitResumeIndexBuilder(intake, p.projection, r.Selection)
	stopOnEffect()
	stop()
	release()
	if err != nil {
		return c.reconcileIndexOutcome(p, r, err)
	}
	for {
		_, turn, done, changed, err := c.beginWithWait(effect, r.Selection.ProjectID)
		if authorityConflict(err) {
			select {
			case <-effect.Done():
				return c.reconcileIndexOutcome(p, r, effect.Err())
			case <-changed:
			case <-q.wake:
			}
			continue
		}
		if err != nil {
			return c.reconcileIndexOutcome(p, r, err)
		}
		current, found, err := waitIndexRetained(turn, q, r.Selection.BuildID)
		if err == nil && (!found || current.State != "running" || current.Generation != r.Generation) {
			err = context.Canceled
		}
		if err == nil {
			_, err = b.step(turn, projectionStepBudget)
		}
		if err == nil && b.done {
			err = p.projection.publish(turn, b)
		}
		done()
		var publicationWait projectionContention
		if errors.As(err, &publicationWait) {
			if p.indexTurnFault != nil {
				if e := p.indexTurnFault("index_turn_complete"); e != nil {
					return c.reconcileIndexOutcome(p, r, e)
				}
			}
			select {
			case <-effect.Done():
				return c.reconcileIndexOutcome(p, r, effect.Err())
			case <-publicationWait.changed:
			}
			continue // recheck durable cancellation/current generation before retry
		}
		if err == nil && p.indexTurnFault != nil {
			err = p.indexTurnFault("index_turn_complete")
		}
		if err != nil || b.done {
			return c.reconcileIndexOutcome(p, r, err)
		}
	}
}

// The actual builder/publication has joined before reconciliation. Exact
// catalog proof wins over late cancellation; ambiguity cannot become failure
// or permission for a duplicate publication. Source content is never changed.
func (c *Coordinator) reconcileIndexOutcome(p *coordinatedProject, r indexJobRecord, effectErr error) (err error) {
	defer func() {
		if err != nil {
			p.indexJobs.terminalUncertain.Store(true)
		}
	}()
	budget := CoordinatorCallBudget
	if p.indexProofBudget > 0 && p.indexProofBudget < budget {
		budget = p.indexProofBudget
	}
	proofCtx, cancel := context.WithTimeout(context.WithoutCancel(c.lifetime), budget)
	defer cancel()
	proof, published, proofErr := waitIndexProof(proofCtx, p.projection, r.Selection)
	if proofErr == nil && published {
		return finishIndexMetadata(proofCtx, p.indexJobs, r.Selection.BuildID, "succeeded", "", &proof)
	}
	current, found, err := waitIndexRetained(proofCtx, p.indexJobs, r.Selection.BuildID)
	if err != nil {
		return err
	}
	if !found {
		return Error{IndexJobsCorrupt}
	}
	state, code := "failed", "operation_failed"
	if proofErr != nil {
		state, code = "interrupted", "outcome_uncertain"
	} else if current.CancellationRequested {
		state, code = "cancelled", "cancelled"
	} else if c.lifetime.Err() != nil {
		state, code = "interrupted", "interrupted"
	} else if errors.Is(effectErr, context.DeadlineExceeded) {
		state, code = "interrupted", "timeout"
	} else {
		var storage Error
		var contract authority.Error
		if errors.As(effectErr, &storage) && indexJobCode(string(storage.Code)) {
			code = string(storage.Code)
		} else if errors.As(effectErr, &contract) && indexJobCode(string(contract.Code)) {
			code = string(contract.Code)
		}
	}
	return finishIndexMetadata(proofCtx, p.indexJobs, r.Selection.BuildID, state, code, nil)
}

func finishIndexMetadata(ctx context.Context, q *indexQueue, id authority.UUID, state, code string, proof *indexPublication) (err error) {
	// Every failed terminal exit, including lock/proof timeout, seals controls
	// without waiting for the lock that prevented acknowledgement. Only a new
	// qualified owner can clear this marker from actual retained bytes.
	defer func() {
		if err != nil {
			q.terminalUncertain.Store(true)
		}
	}()
	for {
		changed := q.mu.changed()
		r, found, err := q.retained(ctx, id)
		if err == nil && !found {
			return Error{IndexJobsCorrupt}
		}
		if err == nil && r.State == "succeeded" {
			return nil
		}
		if err == nil {
			_, err = q.transition(ctx, id, r.Generation, func(next *indexJobRecord) { next.State, next.Code, next.Publication = state, code, proof }, nil)
		}
		if !authorityConflict(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
