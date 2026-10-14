package localauthority

import (
	"context"
	"sync"
	"time"

	"gitcode-mcp/internal/authority"
)

const CoordinatorProjectLimit = 16
const CoordinatorCallBudget = 10 * time.Second

// ProjectBinding is trusted, process-local launch input, not an IPC request,
// cache binding, environment-selected root or serialized permission to write.
type ProjectBinding struct {
	ProjectID authority.UUID
	Directory string
}

type coordinatedProject struct {
	store                     *Store
	turn                      releaseMutex
	jobs                      *recoveryQueue
	jobsError                 Code
	projection                *projectionIndex
	projectionError           Code
	indexJobs                 *indexQueue
	indexJobsError            Code
	repair                    *projectionRepairStore
	repairError               Code
	activeIndex               authority.UUID
	indexCancel               context.CancelFunc
	indexAttemptGeneration    uint64
	indexTurnFault            func(string) error // private deterministic fixture, never configured
	indexProofBudget          time.Duration      // private short-deadline fixture; zero uses production call budget
	activeRecovery            authority.UUID     // guarded by coordinator admission mutex
	recoveryCancel            context.CancelFunc
	recoveryAttemptGeneration uint64
}

// Coordinator owns only local authority. It has no remote manager, credential
// factory, Admin listener, provider setup, network client or implicit enrollment.
// Native launch and authenticated private transport must preserve its origin.
type Coordinator struct {
	mu          releaseMutex
	projects    map[authority.UUID]*coordinatedProject
	order       []*coordinatedProject
	lifetime    context.Context
	cancel      context.CancelFunc
	active      sync.WaitGroup
	jobWorkers  sync.WaitGroup
	jobsStarted bool
	closing     bool
	done        chan struct{}
	closeErr    error
}

// Available is a non-mutating core-admission check, not native launch/network
// attestation. It performs no filesystem, provider or credential discovery.
func (c *Coordinator) Available() bool {
	if c == nil || c.lifetime == nil || c.done == nil {
		return false
	}
	// The cohort is immutable after construction. Cancellation is the shared
	// shutdown fence; admission-mutex contention is a typed operation conflict,
	// not permission to advertise an absent/incompatible runtime.
	return c.lifetime.Err() == nil && len(c.projects) > 0
}

// NewStrictCoordinator consumes existing ownership after trusted setup. All
// bindings are inspected before ANY writer opens. A mixed/loopback cohort is
// refused; the ordinary remote coordinator is never loaded as a fallback.
// This constructor alone is not a native process/no-network qualification.
func NewStrictCoordinator(ctx context.Context, prepared *PreparedRegistry, bindings []ProjectBinding) (*Coordinator, error) {
	return newStrictCoordinator(ctx, prepared, bindings, true)
}

func newStrictCoordinator(ctx context.Context, prepared *PreparedRegistry, bindings []ProjectBinding, startJobs bool) (*Coordinator, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if prepared == nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if len(bindings) < 1 || len(bindings) > CoordinatorProjectLimit {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	if !prepared.mu.TryLock() {
		return nil, authority.Error{Code: authority.Conflict}
	}
	valid := !prepared.closed && prepared.registry != nil && prepared.directory != nil
	prepared.mu.Unlock()
	if !valid {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	bootstrap, cancel := context.WithTimeout(ctx, CoordinatorCallBudget)
	defer cancel()
	seen := map[authority.UUID]bool{}
	// Intake is copied; callers cannot retarget an already admitted project.
	bindings = append([]ProjectBinding(nil), bindings...)
	for _, b := range bindings {
		if !b.ProjectID.Valid() || seen[b.ProjectID] {
			return nil, authority.Error{Code: authority.InvalidContract}
		}
		seen[b.ProjectID] = true
	}
	for _, b := range bindings {
		s, err := OpenReadOnly(bootstrap, b.Directory, b.ProjectID)
		if err != nil {
			return nil, err
		}
		policy := s.manifest.Network
		err = s.Close()
		if err != nil {
			return nil, err
		}
		if policy.DataEgress != "deny" || policy.ControlTransport != "ipc_only" {
			return nil, authority.Error{Code: authority.PolicyDenied}
		}
	}
	lifetime, stop := context.WithCancel(ctx)
	c := &Coordinator{projects: map[authority.UUID]*coordinatedProject{}, lifetime: lifetime, cancel: stop, done: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			stop()
			for _, p := range c.order {
				if p.jobs != nil {
					_ = p.jobs.close()
				}
				if p.indexJobs != nil {
					_ = p.indexJobs.close()
				}
				if p.projection != nil {
					_ = p.projection.close()
				}
				if p.repair != nil {
					_ = p.repair.close()
				}
				_ = p.store.Close()
			}
		}
	}()
	for _, b := range bindings {
		s, err := prepared.OpenWriter(bootstrap, b.Directory, b.ProjectID)
		if err != nil {
			return nil, err
		}
		// Recheck the policy from the writer's verified immutable manifest, not
		// only the prior inspection (physical replacement may occur between).
		if s.manifest.Network.DataEgress != "deny" || s.manifest.Network.ControlTransport != "ipc_only" {
			_ = s.Close()
			return nil, authority.Error{Code: authority.PolicyDenied}
		}
		p := &coordinatedProject{store: s}
		c.projects[b.ProjectID] = p
		c.order = append(c.order, p)
	}
	if bootstrap.Err() != nil {
		return nil, bootstrap.Err()
	}
	for _, p := range c.order {
		jobs, err := newRecoveryQueue(bootstrap, p.store, nil)
		p.jobs = jobs
		if err != nil {
			p.jobsError = recoveryJobErrorCode(err)
		}
		p.projection, err = openProjection(bootstrap, p.store)
		if err != nil {
			p.projectionError = indexStorageErrorCode(err, ProjectionUncertain)
		}
		p.indexJobs, err = openIndexQueue(bootstrap, p.store)
		if err != nil {
			p.indexJobsError = indexStorageErrorCode(err, IndexJobsUncertain)
		}
		p.repair, err = openProjectionRepair(bootstrap, p.store)
		if err != nil {
			p.repairError = indexStorageErrorCode(err, RepairUncertain)
		}
	}
	if bootstrap.Err() != nil {
		return nil, bootstrap.Err()
	}
	ok = true
	go c.shutdown()
	if startJobs {
		if err := c.activateRecoveryWorkers(); err != nil {
			_ = c.Close(context.Background())
			return nil, err
		}
	}
	return c, nil
}

func (c *Coordinator) shutdown() {
	<-c.lifetime.Done()
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	// Add is protected by the same admission mutex and closing/lifetime fence.
	// Keep writer ownership until every admitted operation has joined, even if
	// a caller's bounded Close has already reported a deadline.
	c.jobWorkers.Wait()
	c.active.Wait()
	for _, p := range c.order {
		if p.jobs != nil {
			if err := p.jobs.close(); err != nil && c.closeErr == nil {
				c.closeErr = err
			}
		}
		if p.indexJobs != nil {
			if err := p.indexJobs.close(); err != nil && c.closeErr == nil {
				c.closeErr = err
			}
		}
		if p.projection != nil {
			if err := p.projection.close(); err != nil && c.closeErr == nil {
				c.closeErr = err
			}
		}
		if p.repair != nil {
			if err := p.repair.close(); err != nil && c.closeErr == nil {
				c.closeErr = err
			}
		}
		if err := p.store.Close(); err != nil && c.closeErr == nil {
			c.closeErr = err
		}
	}
	close(c.done)
}

func (c *Coordinator) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if c.cancel == nil || c.done == nil {
		return authority.Error{Code: authority.Unavailable}
	}
	c.cancel()
	select {
	case <-c.done:
		return c.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) begin(ctx context.Context, project authority.UUID) (*Store, context.Context, func(), error) {
	s, call, release, _, err := c.beginWithWait(ctx, project)
	return s, call, release, err
}

func (c *Coordinator) beginWithWait(ctx context.Context, project authority.UUID) (*Store, context.Context, func(), <-chan struct{}, error) {
	if ctx.Err() != nil {
		return nil, nil, nil, nil, ctx.Err()
	}
	if !project.Valid() {
		return nil, nil, nil, nil, authority.Error{Code: authority.InvalidContract}
	}
	if c == nil || c.lifetime == nil || c.done == nil {
		return nil, nil, nil, nil, authority.Error{Code: authority.Unavailable}
	}
	admissionChanged := c.mu.changed()
	if !c.mu.TryLock() {
		return nil, nil, nil, admissionChanged, authority.Error{Code: authority.Conflict}
	}
	defer c.mu.Unlock()
	if c.closing || c.lifetime.Err() != nil {
		return nil, nil, nil, nil, authority.Error{Code: authority.Unavailable}
	}
	p := c.projects[project]
	if p == nil {
		return nil, nil, nil, nil, authority.Error{Code: authority.NoAuthority}
	}
	turnChanged := p.turn.changed()
	if !p.turn.TryLock() {
		return nil, nil, nil, turnChanged, authority.Error{Code: authority.Conflict}
	}
	c.active.Add(1)
	call, cancel := context.WithTimeout(ctx, CoordinatorCallBudget)
	stop := context.AfterFunc(c.lifetime, cancel)
	var once sync.Once
	release := func() {
		once.Do(func() {
			stop()
			cancel()
			p.turn.Unlock()
			c.active.Done()
		})
	}
	return p.store, call, release, nil, nil
}

func coordinated[T any](c *Coordinator, ctx context.Context, project authority.UUID, run func(context.Context, *Store) (T, error)) (T, error) {
	var zero T
	s, call, release, err := c.begin(ctx, project)
	if err != nil {
		return zero, err
	}
	defer release()
	if call.Err() != nil {
		return zero, call.Err()
	}
	return run(call, s)
}

func (c *Coordinator) Status(ctx context.Context, project authority.UUID) (authority.ProjectStatus, error) {
	return coordinated(c, ctx, project, func(ctx context.Context, s *Store) (authority.ProjectStatus, error) {
		// Tracker readiness is independent of Git knowledge readiness. A
		// pending/damaged document namespace must not quiesce unrelated tracker
		// work. KnowledgeStatus reports its explicit dependent-write fence.
		return s.Status(ctx)
	})
}

func (c *Coordinator) Read(ctx context.Context, req authority.ReadRequest) (authority.ReadResult, error) {
	if err := req.Validate(); err != nil {
		return authority.ReadResult{}, err
	}
	return coordinated(c, ctx, req.ProjectID, func(ctx context.Context, s *Store) (authority.ReadResult, error) { return s.Read(ctx, req) })
}

func (c *Coordinator) KnowledgeStatus(ctx context.Context, project authority.UUID) (KnowledgeState, error) {
	return coordinated(c, ctx, project, func(ctx context.Context, s *Store) (KnowledgeState, error) { return s.KnowledgeStatus(ctx, project) })
}

func (c *Coordinator) Mutate(ctx context.Context, req authority.MutationRequest, actor Actor) (authority.MutationReceipt, error) {
	if err := req.Validate(); err != nil {
		return authority.MutationReceipt{}, err
	}
	return coordinated(c, ctx, req.Intent.ProjectID, func(ctx context.Context, s *Store) (authority.MutationReceipt, error) {
		return s.Mutate(ctx, req, actor)
	})
}

func (c *Coordinator) MutateDocument(ctx context.Context, req authority.DocumentMutationRequest, actor Actor) (authority.DocumentMutationResult, error) {
	if err := req.Validate(); err != nil {
		return authority.DocumentMutationResult{}, err
	}
	admitted := false
	result, err := coordinated(c, ctx, req.Intent.ProjectID, func(ctx context.Context, s *Store) (authority.DocumentMutationResult, error) {
		admitted = true
		return s.MutateDocument(ctx, req, actor)
	})
	// Only explicit external document writes wake proof reconciliation of
	// already admitted jobs, including ambiguous source outcomes. Reads and
	// the worker's own turn releases must not generate a self-waking loop.
	if admitted && c != nil {
		if p := c.projects[req.Intent.ProjectID]; p != nil && p.jobs != nil {
			p.jobs.signal()
		}
	}
	return result, err
}

func (c *Coordinator) LexicalSearch(ctx context.Context, req LexicalRequest) (LexicalResult, error) {
	return coordinated(c, ctx, req.ProjectID, func(ctx context.Context, s *Store) (LexicalResult, error) { return s.LexicalSearch(ctx, req) })
}
