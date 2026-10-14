package localauthority

import (
	"context"
	"errors"

	"gitcode-mcp/internal/authority"
)

func indexStorageErrorCode(err error, fallback Code) Code {
	var e Error
	if errors.As(err, &e) {
		return e.Code
	}
	return fallback
}

func (c *Coordinator) beginIndexControl(ctx context.Context, project authority.UUID) (*coordinatedProject, context.Context, func(), error) {
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
	if p.indexJobs == nil {
		code := p.indexJobsError
		if code == "" {
			code = IndexJobsUncertain
		}
		return nil, nil, nil, Error{code}
	}
	c.active.Add(1)
	call, cancel := context.WithTimeout(ctx, CoordinatorCallBudget)
	stop := context.AfterFunc(c.lifetime, cancel)
	return p, call, func() { stop(); cancel(); c.active.Done() }, nil
}

func (c *Coordinator) EnqueueIndex(ctx context.Context, req IndexJobRequest, actor Actor) (IndexJobView, error) {
	if req.Validate() != nil || !actor.valid() {
		return IndexJobView{}, authority.Error{Code: authority.InvalidContract}
	}
	p, call, release, err := c.beginIndexControl(ctx, req.ProjectID)
	if err != nil {
		return IndexJobView{}, err
	}
	defer release()
	if r, found, err := p.indexJobs.byKey(call, req); err != nil {
		return IndexJobView{}, err
	} else if found {
		p.indexJobs.signal()
		return indexJobView(r), nil
	}
	if p.projection == nil {
		code := p.projectionError
		if code == "" {
			code = ProjectionUncertain
		}
		return IndexJobView{}, Error{code}
	}
	s, effect, done, err := c.begin(call, req.ProjectID)
	if err != nil {
		return IndexJobView{}, err
	}
	defer done()
	if p.repair == nil {
		return IndexJobView{}, Error{RepairUncertain}
	}
	if err := p.repair.allowIndex(effect); err != nil {
		return IndexJobView{}, err
	}
	status, err := s.Status(effect)
	if err != nil {
		return IndexJobView{}, err
	}
	if status.Readiness != "ready" || status.AuthorityGeneration != req.Sequence {
		return IndexJobView{}, authority.Error{Code: authority.Conflict}
	}
	b, err := p.projection.newBuilder(effect, req.Sequence)
	if err != nil {
		return IndexJobView{}, err
	}
	r, err := p.indexJobs.admit(effect, req, selectionForIndexBuilder(b), actor)
	if err != nil {
		return IndexJobView{}, err
	}
	p.indexJobs.signal()
	return indexJobView(r), nil
}

func (c *Coordinator) IndexJob(ctx context.Context, project, id authority.UUID) (IndexJobView, error) {
	if !id.Valid() {
		return IndexJobView{}, authority.Error{Code: authority.InvalidContract}
	}
	p, call, release, err := c.beginIndexControl(ctx, project)
	if err != nil {
		return IndexJobView{}, err
	}
	defer release()
	r, found, err := p.indexJobs.retained(call, id)
	if err != nil {
		return IndexJobView{}, err
	}
	if !found {
		return IndexJobView{}, Error{IndexJobNotFound}
	}
	return indexJobView(r), nil
}

func (c *Coordinator) indexAction(ctx context.Context, req IndexJobAction, actor Actor, retry bool) (IndexJobView, error) {
	if req.Validate() != nil || !actor.valid() {
		return IndexJobView{}, authority.Error{Code: authority.InvalidContract}
	}
	p, call, release, err := c.beginIndexControl(ctx, req.ProjectID)
	if err != nil {
		return IndexJobView{}, err
	}
	defer release()
	q := p.indexJobs
	r, found, err := q.retained(call, req.JobID)
	if err != nil {
		return IndexJobView{}, err
	}
	if !found {
		return IndexJobView{}, Error{IndexJobNotFound}
	}
	if retry && r.RetryFrom == req.ExpectedGeneration || !retry && r.CancelFrom == req.ExpectedGeneration && r.CancellationRequested {
		q.signal()
		return indexJobView(r), nil
	}
	if r.Generation != req.ExpectedGeneration {
		return IndexJobView{}, authority.Error{Code: authority.Conflict}
	}
	if retry && r.State == "succeeded" {
		return indexJobView(r), nil
	}
	if retry && r.State != "succeeded" && r.State != "failed" && r.State != "interrupted" && r.State != "cancelled" {
		return IndexJobView{}, authority.Error{Code: authority.Conflict}
	}
	if retry && (r.State == "failed" || r.State == "interrupted" || r.State == "cancelled") && r.Attempts >= maxIndexAttempts {
		return IndexJobView{}, Error{Capacity}
	}
	if retry {
		// Historical permission replay above grants no new attempt. Only an
		// actual new retry is serialized against repair's owned source turn.
		_, retryCall, done, err := c.begin(call, req.ProjectID)
		if err != nil {
			return IndexJobView{}, err
		}
		defer done()
		call = retryCall
		if p.repair == nil {
			return IndexJobView{}, Error{RepairUncertain}
		}
		if err := p.repair.allowIndex(call); err != nil {
			return IndexJobView{}, err
		}
	}
	changed := false
	ambiguous := false
	after, err := q.transition(call, req.JobID, req.ExpectedGeneration, func(next *indexJobRecord) {
		if retry {
			if next.State != "failed" && next.State != "interrupted" && next.State != "cancelled" {
				return
			}
			next.State, next.Code, next.RetryFrom = "queued", "", next.Generation
			next.CancellationRequested = false
		} else {
			if next.State != "queued" && next.State != "running" {
				return
			}
			next.CancelFrom, next.CancellationRequested = next.Generation, true
			if next.State == "queued" {
				next.State, next.Code = "cancelled", "cancelled"
			} else {
				next.State = "cancelling"
			}
		}
		changed = true
		next.ActionActorHash = jobActorHash(req.ProjectID, actor)
	}, &ambiguous)
	if !retry && changed && (err == nil || ambiguous) {
		c.mu.Lock()
		cancel := p.indexCancel
		if p.activeIndex != req.JobID || p.indexAttemptGeneration != req.ExpectedGeneration {
			cancel = nil
		}
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	if err == nil || ambiguous {
		q.signal()
	}
	if err != nil {
		return IndexJobView{}, err
	}
	return indexJobView(after), nil
}

func (c *Coordinator) CancelIndex(ctx context.Context, req IndexJobAction, actor Actor) (IndexJobView, error) {
	return c.indexAction(ctx, req, actor, false)
}

func (c *Coordinator) RetryIndex(ctx context.Context, req IndexJobAction, actor Actor) (IndexJobView, error) {
	return c.indexAction(ctx, req, actor, true)
}

func (c *Coordinator) setIndexAttempt(p *coordinatedProject, r indexJobRecord, cancel context.CancelFunc) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.lifetime.Err() != nil {
		return false
	}
	p.activeIndex, p.indexAttemptGeneration, p.indexCancel = r.Selection.BuildID, r.Generation, cancel
	return true
}

func (c *Coordinator) clearIndexAttempt(p *coordinatedProject, r indexJobRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.activeIndex == r.Selection.BuildID && p.indexAttemptGeneration == r.Generation {
		p.activeIndex, p.indexAttemptGeneration, p.indexCancel = "", 0, nil
	}
}
