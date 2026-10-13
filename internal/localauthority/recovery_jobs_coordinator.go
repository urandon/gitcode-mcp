package localauthority

import (
	"context"
	"errors"

	"gitcode-mcp/internal/authority"
)

func recoveryJobErrorCode(err error) Code {
	var e Error
	if errors.As(err, &e) {
		return e.Code
	}
	return JobsUncertain
}

func (c *Coordinator) activateRecoveryWorkers() error {
	if c == nil || c.lifetime == nil || c.done == nil {
		return authority.Error{Code: authority.Unavailable}
	}
	if !c.mu.TryLock() {
		return authority.Error{Code: authority.Conflict}
	}
	defer c.mu.Unlock()
	if c.closing || c.lifetime.Err() != nil {
		return authority.Error{Code: authority.Unavailable}
	}
	if c.jobsStarted {
		return nil
	}
	c.jobsStarted = true
	for _, p := range c.order {
		if p.indexJobs != nil && p.projection != nil {
			c.jobWorkers.Add(1)
			go c.indexWorker(p)
			p.indexJobs.signal()
		}
		if p.jobs == nil {
			continue
		}
		c.jobWorkers.Add(1) // registration shares the shutdown closing fence
		go c.recoveryWorker(p)
		p.jobs.signal()
	}
	return nil
}

// Job control deliberately does not take the worker-held source effect turn.
// Its lifetime registration protects retained metadata descriptors until join.
func (c *Coordinator) beginRecoveryControl(ctx context.Context, project authority.UUID) (*coordinatedProject, context.Context, func(), error) {
	if ctx.Err() != nil {
		return nil, nil, nil, ctx.Err()
	}
	if !project.Valid() {
		return nil, nil, nil, authority.Error{Code: authority.InvalidContract}
	}
	if c == nil || c.lifetime == nil || c.done == nil {
		return nil, nil, nil, authority.Error{Code: authority.Unavailable}
	}
	if !c.mu.TryLock() {
		return nil, nil, nil, authority.Error{Code: authority.Conflict}
	}
	defer c.mu.Unlock()
	if c.closing || c.lifetime.Err() != nil || !c.jobsStarted {
		return nil, nil, nil, authority.Error{Code: authority.Unavailable}
	}
	p := c.projects[project]
	if p == nil {
		return nil, nil, nil, authority.Error{Code: authority.NoAuthority}
	}
	if p.jobs == nil {
		code := p.jobsError
		if code == "" {
			code = JobsUncertain
		}
		return nil, nil, nil, Error{code}
	}
	c.active.Add(1)
	call, cancel := context.WithTimeout(ctx, CoordinatorCallBudget)
	stop := context.AfterFunc(c.lifetime, cancel)
	release := func() { stop(); cancel(); c.active.Done() }
	return p, call, release, nil
}

type RecoveryJobAdmission struct {
	Schema      int                        `json:"schema"`
	ProjectID   authority.UUID             `json:"project_id"`
	OperationID authority.UUID             `json:"operation_id"`
	Outcome     string                     `json:"outcome"`
	Job         *RecoveryJobView           `json:"job,omitempty"`
	Receipt     *authority.MutationReceipt `json:"receipt,omitempty"`
}

func (a RecoveryJobAdmission) Validate() error {
	if a.Schema != 1 || !a.ProjectID.Valid() || !a.OperationID.Valid() {
		return authority.Error{Code: authority.InvalidContract}
	}
	switch a.Outcome {
	case "queued", "retained":
		if a.Job == nil || a.Job.Validate() != nil || a.Job.ProjectID != a.ProjectID || a.Job.JobID != a.OperationID || a.Receipt != nil {
			return authority.Error{Code: authority.InvalidContract}
		}
		if a.Outcome == "queued" && (a.Job.State != "queued" || a.Job.Generation != 1 || a.Job.Attempts != 0) {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "already_settled":
		if a.Job != nil || a.Receipt == nil || a.Receipt.Validate() != nil || a.Receipt.ProjectID != a.ProjectID || a.Receipt.OperationID != a.OperationID || a.Receipt.Citation.Kind != authority.Document || !a.Receipt.Replayed {
			return authority.Error{Code: authority.InvalidContract}
		}
	default:
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func (c *Coordinator) EnqueueRecovery(ctx context.Context, req RecoveryJobRequest, actor Actor) (RecoveryJobAdmission, error) {
	var zero RecoveryJobAdmission
	if err := req.Validate(); err != nil {
		return zero, err
	}
	if !actor.valid() {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	p, call, release, err := c.beginRecoveryControl(ctx, req.ProjectID)
	if err != nil {
		return zero, err
	}
	defer release()
	view, found, err := p.jobs.retained(call, req.OperationID)
	if err != nil {
		return zero, err
	}
	if found {
		// Replaying retained admission never upgrades its original source
		// selection, even if this caller has a later generation observation.
		// Explicit admission replay may wake already-authorized work or
		// proof-only reconciliation; it never retries an inactive source effect.
		p.jobs.signal()
		return RecoveryJobAdmission{Schema: 1, ProjectID: req.ProjectID, OperationID: req.OperationID, Outcome: "retained", Job: &view}, nil
	}
	s, effect, done, err := c.begin(call, req.ProjectID)
	if err != nil {
		return zero, err
	}
	defer done()
	o, err := s.inspectRecoveryOperation(effect, req.ProjectID, req.OperationID, req.ObservedGeneration)
	if err != nil {
		return zero, err
	}
	if existing, found, e := p.jobs.retained(effect, req.OperationID); e != nil {
		return zero, e
	} else if found {
		p.jobs.signal()
		return RecoveryJobAdmission{Schema: 1, ProjectID: req.ProjectID, OperationID: req.OperationID, Outcome: "retained", Job: &existing}, nil
	}
	if o.Phase == "settled" {
		result, err := s.recoverSelectedDocument(effect, recoverySelection{project: req.ProjectID, operation: o.ID, intent: o.Intent, generation: req.ObservedGeneration})
		if err != nil {
			return zero, err
		}
		receipt := result.Receipt
		return RecoveryJobAdmission{Schema: 1, ProjectID: req.ProjectID, OperationID: req.OperationID, Outcome: "already_settled", Receipt: &receipt}, nil
	}
	view, err = p.jobs.admit(effect, o, actor)
	if err != nil {
		return zero, err
	}
	p.jobs.signal()
	return RecoveryJobAdmission{Schema: 1, ProjectID: req.ProjectID, OperationID: req.OperationID, Outcome: "queued", Job: &view}, nil
}

func (c *Coordinator) RecoveryJob(ctx context.Context, project, id authority.UUID) (RecoveryJobView, error) {
	if !id.Valid() {
		return RecoveryJobView{}, authority.Error{Code: authority.InvalidContract}
	}
	p, call, release, err := c.beginRecoveryControl(ctx, project)
	if err != nil {
		return RecoveryJobView{}, err
	}
	defer release()
	view, found, err := p.jobs.retained(call, id)
	if err != nil {
		return RecoveryJobView{}, err
	}
	if !found {
		return RecoveryJobView{}, Error{JobNotFound}
	}
	return view, nil
}

func (c *Coordinator) CancelRecovery(ctx context.Context, req RecoveryJobAction, actor Actor) (RecoveryJobView, error) {
	if err := req.Validate(); err != nil {
		return RecoveryJobView{}, err
	}
	p, call, release, err := c.beginRecoveryControl(ctx, req.ProjectID)
	if err != nil {
		return RecoveryJobView{}, err
	}
	defer release()
	attempted := false
	view, err := p.jobs.action(call, req, actor, false, &attempted)
	if attempted || err == nil && view.State == "cancelling" {
		// Even an ambiguous metadata ack conservatively stops this exact
		// attempt, never a newer explicitly retried attempt of the same job.
		c.cancelRecoveryAttempt(p, req.JobID, req.ExpectedGeneration)
	}
	if attempted || err == nil {
		p.jobs.signal()
	}
	return view, err
}

func (c *Coordinator) RetryRecovery(ctx context.Context, req RecoveryJobAction, actor Actor) (RecoveryJobView, error) {
	if err := req.Validate(); err != nil {
		return RecoveryJobView{}, err
	}
	p, call, release, err := c.beginRecoveryControl(ctx, req.ProjectID)
	if err != nil {
		return RecoveryJobView{}, err
	}
	defer release()
	view, err := p.jobs.action(call, req, actor, true, nil)
	if err == nil {
		p.jobs.signal()
	}
	return view, err
}

func (c *Coordinator) cancelRecoveryAttempt(p *coordinatedProject, id authority.UUID, generation uint64) {
	c.mu.Lock()
	var cancel context.CancelFunc
	if p.activeRecovery == id && p.recoveryAttemptGeneration == generation {
		cancel = p.recoveryCancel
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Coordinator) setRecoveryAttempt(p *coordinatedProject, id authority.UUID, generation uint64, cancel context.CancelFunc) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.lifetime.Err() != nil {
		return false
	}
	p.activeRecovery, p.recoveryAttemptGeneration, p.recoveryCancel = id, generation, cancel
	return true
}

func (c *Coordinator) clearRecoveryAttempt(p *coordinatedProject, id authority.UUID, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.activeRecovery == id && p.recoveryAttemptGeneration == generation {
		p.activeRecovery = ""
		p.recoveryAttemptGeneration = 0
		p.recoveryCancel = nil
	}
}
