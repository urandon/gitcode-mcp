package localauthority

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"gitcode-mcp/internal/authority"
)

// RuntimeBinding is a trusted in-process handle property, not an IPC discovery
// method. Native parents retain their expectation from completed preparation.
func (c *Coordinator) RuntimeBinding() string {
	if c == nil {
		return ""
	}
	return c.runtimeBinding
}

// RuntimeServingLease separates current listening proof from retained handler
// admission, so a replacement cannot exceed the global server handler bound.
type RuntimeServingLease struct {
	closed   atomic.Bool
	released atomic.Bool // admission ownership persists through handler join
}

// Seal stops this exact listening proof without releasing handler admission.
func (l *RuntimeServingLease) Seal() {
	if l != nil {
		l.closed.Store(true)
	}
}
func (l *RuntimeServingLease) Close() {
	if l != nil {
		l.Seal()
		l.released.Store(true)
	}
}

// AdmitServing follows validated listener admission; construction/bootstrap is
// not serving proof. The caller closes the lease only after handler join.
func (c *Coordinator) AdmitServing(ctx context.Context) (*RuntimeServingLease, error) {
	lease, publication, err := c.admitServing(ctx)
	publication.publish()
	return lease, err
}

func (c *Coordinator) admitServing(ctx context.Context) (*RuntimeServingLease, *runtimePublication, error) {
	if c == nil || c.lifetime == nil || c.done == nil {
		return nil, nil, authority.Error{Code: authority.Unavailable}
	}
	ctx, cancel := context.WithTimeout(ctx, CoordinatorCallBudget)
	defer cancel()
	for {
		changed := c.mu.changed()
		if c.mu.TryLock() {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if !c.jobsStarted || c.closing || c.lifetime.Err() != nil {
		return nil, nil, authority.Error{Code: authority.Unavailable}
	}
	if c.serving != nil && !c.serving.released.Load() {
		return nil, nil, authority.Error{Code: authority.Conflict}
	}
	c.serving = &RuntimeServingLease{}
	publication := c.runtimePublication("serving")
	if publication != nil {
		// Add before releasing admission, never after shutdown can enter Wait.
		c.active.Add(1)
		publication.joined = &c.active
	}
	return c.serving, publication, nil
}

func healthDegradation(code Code) string {
	switch code {
	case Corrupt, ProjectionCorrupt, JobsCorrupt, IndexJobsCorrupt, RepairCorrupt:
		return "corrupt"
	}
	return "uncertain"
}

// RuntimeHealth owns one source turn and all existing component locks in a
// fixed nonblocking order: source, recovery, index intent, repair, projection.
// It never provisions, reopens, syncs, repairs, enqueues or publishes anything.
func (c *Coordinator) RuntimeHealth(ctx context.Context, req RuntimeHealthRequest) (RuntimeHealthView, error) {
	var zero RuntimeHealthView
	if req.Validate() != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	if c == nil || c.runtimeBinding != req.RuntimeBinding {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	s, call, release, err := c.begin(ctx, req.ProjectID)
	if err != nil {
		return zero, err
	}
	defer release()
	p := c.projects[req.ProjectID]
	if !s.mu.TryLock() {
		return zero, authority.Error{Code: authority.Conflict}
	}
	defer s.mu.Unlock()
	for _, m := range []*releaseMutex{healthRecoveryMutex(p), healthIndexMutex(p), healthRepairMutex(p), healthProjectionMutex(p)} {
		if m == nil {
			continue
		}
		if !m.TryLock() {
			return zero, authority.Error{Code: authority.Conflict}
		}
		defer m.Unlock()
	}
	if call.Err() != nil {
		return zero, call.Err()
	}
	if s.closed || s.lease == nil {
		return zero, authority.Error{Code: authority.Unavailable}
	}
	if err = s.verifyPhysicalBinding(); err != nil {
		if call.Err() != nil {
			return zero, call.Err()
		}
		s.fenced = true
		return zero, Error{Corrupt}
	}
	v := RuntimeHealthView{Schema: 1, ProjectID: req.ProjectID, RuntimeBinding: c.runtimeBinding, Phase: "starting",
		Tracker:   RuntimeTrackerHealth{State: "unavailable", Writer: "unavailable"},
		Knowledge: RuntimeKnowledgeHealth{State: "unavailable"}, Projection: RuntimeProjectionHealth{State: "uncertain"}}
	identityErr := s.verifyIdentity(call, req.ProjectID)
	if call.Err() != nil {
		return zero, call.Err()
	}
	if identityErr != nil {
		var e Error
		if errors.As(identityErr, &e) && e.Code == Corrupt {
			s.fenced, s.logicalFenced = true, true
			v.Tracker.State = "corrupt"
		}
	} else {
		knowledge, e := s.knowledgeStatusLocked(call, req.ProjectID)
		if call.Err() != nil {
			return zero, call.Err()
		}
		if e == nil {
			v.Knowledge = RuntimeKnowledgeHealth{State: knowledge.Phase, Pending: knowledge.Pending != nil}
		} else {
			var domain Error
			if errors.As(e, &domain) && domain.Code == Corrupt {
				s.fenced, s.logicalFenced = true, true
			}
		}
		// Knowledge SQL inspection can fence the global writer. Never publish a
		// tracker image captured before that inspection or clear a live fence.
		if s.logicalFenced {
			v.Tracker.State = "corrupt"
			v.Knowledge = RuntimeKnowledgeHealth{State: "unavailable"}
		} else if status, e := s.statusLocked(call); e == nil {
			sequence := status.AuthorityGeneration
			v.Tracker = RuntimeTrackerHealth{State: status.Readiness, Writer: status.WriterState, SourceSequence: &sequence}
		}
	}
	if v.Tracker.State == "ready" {
		if err := verifyWriter(call, s.db); err != nil {
			if call.Err() != nil {
				return zero, call.Err()
			}
			// SQL remains qualified for canonical reads. A transient failed
			// durability admission is not a fabricated persistent recovery fence.
			v.Tracker.State, v.Tracker.Writer = "read_only", "unavailable"
		}
	}
	v.RecoveryJobs = runtimeRecoveryHealth(call, p, v.Tracker.SourceSequence != nil)
	v.IndexJobs = runtimeIndexHealth(call, p, v.Tracker.SourceSequence)
	v.Repair = runtimeRepairHealth(call, p, v.Tracker.SourceSequence)
	v.Projection = runtimeProjectionHealth(call, p, v.Tracker.SourceSequence)
	if runtimeIndexUncertain(p) || v.Repair == "pending" {
		v.Projection = RuntimeProjectionHealth{State: "uncertain"}
	}
	if v.Repair == "restart_required" {
		v.Projection = RuntimeProjectionHealth{State: "restart_required"}
	}
	// Worker identity is separate from terminal ledger rows. The admission
	// mutex is held only for this in-memory snapshot, never during Git scans.
	if !c.mu.TryLock() {
		return zero, authority.Error{Code: authority.Conflict}
	}
	closing := c.closing || c.lifetime.Err() != nil || !c.jobsStarted
	if c.serving != nil {
		v.Phase = "not_serving"
		if !c.serving.closed.Load() {
			v.Phase = "serving"
		}
	}
	v.RecoveryJobs.WorkerActive, v.IndexJobs.WorkerActive = p.activeRecovery != "", p.activeIndex != ""
	c.mu.Unlock()
	if closing {
		return zero, authority.Error{Code: authority.Unavailable}
	}
	if call.Err() != nil {
		return zero, call.Err()
	}
	// Exact physical bindings remain authoritative after all transient reads.
	if s.verifyPhysicalBinding() != nil {
		if call.Err() != nil {
			return zero, call.Err()
		}
		s.fenced = true
		return zero, Error{Corrupt}
	}
	v.RetrievedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if v.Validate() != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	if call.Err() != nil {
		return zero, call.Err()
	}
	return v, nil
}

func runtimeIndexUncertain(p *coordinatedProject) bool {
	q := p.indexJobs
	if q == nil {
		return p.indexJobsError == IndexJobsUncertain
	}
	if q.terminalUncertain.Load() || q.fence == IndexJobsUncertain {
		return true
	}
	for _, r := range q.state.Jobs {
		if r.Code == "outcome_uncertain" || r.Code == string(ProjectionUncertain) {
			return true
		}
	}
	return false
}

func healthRecoveryMutex(p *coordinatedProject) *releaseMutex {
	if p.jobs != nil {
		return &p.jobs.mu
	}
	return nil
}
func healthIndexMutex(p *coordinatedProject) *releaseMutex {
	if p.indexJobs != nil {
		return &p.indexJobs.mu
	}
	return nil
}
func healthRepairMutex(p *coordinatedProject) *releaseMutex {
	if p.repair != nil {
		return &p.repair.mu
	}
	return nil
}
func healthProjectionMutex(p *coordinatedProject) *releaseMutex {
	if p.projection != nil {
		return &p.projection.mu
	}
	return nil
}

func runtimeRecoveryHealth(ctx context.Context, p *coordinatedProject, sourceQualified bool) RuntimeJobsHealth {
	v := RuntimeJobsHealth{State: "unavailable"}
	q := p.jobs
	if q == nil {
		return v
	}
	if q.closed || q.fence != "" {
		v.State = healthDegradation(q.fence)
		return v
	}
	if q.verifyExpected() != nil || !q.state.valid() || q.state.ProjectID != p.store.manifest.ID || q.state.TrackerInstance != p.store.instance || q.state.ManifestHash != manifestHash(p.store.manifest) {
		v.State = "corrupt"
		return v
	}
	if _, _, err := q.stages(ctx); err != nil {
		v.State = healthDegradation(indexStorageErrorCode(err, JobsUncertain))
		return v
	}
	if !sourceQualified {
		return v
	}
	if p.store.validateRecoveryJobTargetsLocked(ctx, q.state) != nil {
		v.State = "corrupt"
		return v
	}
	counts := RuntimeJobCounts{}
	for _, r := range q.state.Jobs {
		if !counts.add(r.View.State) {
			v.State = "corrupt"
			return v
		}
	}
	v.State, v.Counts = "ready", &counts
	return v
}

func runtimeIndexHealth(ctx context.Context, p *coordinatedProject, sequence *uint64) RuntimeJobsHealth {
	v := RuntimeJobsHealth{State: healthDegradation(p.indexJobsError)}
	q := p.indexJobs
	if q == nil {
		return v
	}
	if q.closed || q.fence != "" || q.terminalUncertain.Load() {
		v.State = healthDegradation(q.fence)
		return v
	}
	if q.verifyExpected() != nil {
		v.State = "corrupt"
		return v
	}
	if q.directory == nil {
		return RuntimeJobsHealth{State: "missing"}
	}
	if _, _, err := q.inventory(ctx); err != nil {
		v.State = healthDegradation(indexStorageErrorCode(err, IndexJobsUncertain))
		return v
	}
	if !q.matchesSource(q.state) {
		v.State = "corrupt"
		return v
	}
	if sequence == nil {
		return v
	}
	counts := RuntimeJobCounts{}
	for _, r := range q.state.Jobs {
		if r.Selection.Sequence > *sequence || !counts.add(r.State) {
			v.State = "corrupt"
			return v
		}
	}
	v.State, v.Counts = "ready", &counts
	return v
}

func runtimeRepairHealth(ctx context.Context, p *coordinatedProject, sequence *uint64) string {
	q := p.repair
	if q == nil {
		return healthDegradation(p.repairError)
	}
	if q.restartRequired {
		return "restart_required"
	}
	if q.closed || q.fence != "" {
		return healthDegradation(q.fence)
	}
	if q.verifyExpected() != nil {
		return "corrupt"
	}
	if q.directory == nil {
		return "missing"
	}
	if _, _, err := q.inventory(ctx); err != nil {
		return healthDegradation(indexStorageErrorCode(err, RepairUncertain))
	}
	if !q.state.valid() || q.state.ProjectID != p.store.manifest.ID || q.state.TrackerInstance != p.store.instance || q.state.ManifestHash != manifestHash(p.store.manifest) {
		return "corrupt"
	}
	if sequence == nil {
		return "uncertain"
	}
	for _, r := range q.state.Records {
		if r.Plan.Sequence > *sequence {
			return "corrupt"
		}
	}
	if q.pending() {
		return "pending"
	}
	return "ready"
}

func runtimeProjectionHealth(ctx context.Context, p *coordinatedProject, sequence *uint64) RuntimeProjectionHealth {
	v := RuntimeProjectionHealth{State: healthDegradation(p.projectionError)}
	q := p.projection
	if q == nil {
		return v
	}
	if q.closed || q.fence != "" {
		v.State = healthDegradation(q.fence)
		return v
	}
	if q.verifyExpected() != nil {
		v.State = "corrupt"
		return v
	}
	if q.directory == nil {
		return RuntimeProjectionHealth{State: "missing"}
	}
	if _, _, err := q.inventory(ctx); err != nil {
		v.State = healthDegradation(indexStorageErrorCode(err, ProjectionUncertain))
		return v
	}
	if q.catalog == nil {
		return RuntimeProjectionHealth{State: "missing"}
	}
	catalog := q.catalog
	if !catalog.valid() || catalog.ProjectID != p.store.manifest.ID || catalog.TrackerInstance != p.store.instance || catalog.ManifestHash != manifestHash(p.store.manifest) {
		v.State = "corrupt"
		return v
	}
	if sequence == nil {
		return v
	}
	if catalog.Sequence > *sequence {
		v.State = "corrupt"
		return v
	}
	indexed := catalog.Sequence
	v.State, v.IndexedSequence = "current", &indexed
	if indexed < *sequence {
		v.State = "stale"
	}
	return v
}
