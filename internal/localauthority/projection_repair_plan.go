package localauthority

import (
	"context"
	"encoding/json"
	"math"

	"gitcode-mcp/internal/authority"
)

const projectionRepairByteLimit = MaxProjectionBytes + MaxProjectionStageBytes

// ProjectionRepairPlan is a read-only proposal, never an execution receipt.
// Opaque digests bind current source, physical namespace and durable intent;
// no path, caller key, actor, filename or corrupt file body crosses this view.
// Public transports remain gated by the complete owned apply/replay protocol.
type ProjectionRepairPlan struct {
	Schema           int            `json:"schema"`
	ProjectID        authority.UUID `json:"project_id"`
	PlanID           string         `json:"plan_id"`
	Sequence         uint64         `json:"sequence"`
	LedgerState      string         `json:"ledger_state"`
	LedgerGeneration uint64         `json:"ledger_generation"`
	State            string         `json:"state"`
	Action           string         `json:"action"`
	Blocker          string         `json:"blocker"`
	Files            int            `json:"files"`
	Bytes            int64          `json:"bytes"`
	RestartRequired  bool           `json:"restart_required"`
}

func (p ProjectionRepairPlan) Validate() error {
	if p.Schema != 1 || !p.ProjectID.Valid() || !validDigest(p.PlanID) || p.Sequence > math.MaxInt64 || p.LedgerGeneration > math.MaxInt64 || p.Files < 0 || p.Files > MaxProjectionStages+1 || p.Bytes < 0 || p.Bytes > projectionRepairByteLimit || p.Bytes > int64(p.Files)*MaxProjectionBytes {
		return authority.Error{Code: authority.InvalidContract}
	}
	if !(p.LedgerState == "missing" && p.LedgerGeneration == 0 || p.LedgerState == "ready" && p.LedgerGeneration >= 1) {
		return authority.Error{Code: authority.InvalidContract}
	}
	switch p.State {
	case "ready":
		if p.Action == "quarantine" && p.Blocker == "" && p.RestartRequired {
			return nil
		}
	case "not_needed":
		if p.Action == "none" && p.Blocker == "" && !p.RestartRequired {
			return nil
		}
	case "blocked":
		if p.Action == "none" && (p.Blocker == "index_active" || p.Blocker == "index_uncertain") && p.LedgerState == "ready" && p.LedgerGeneration >= 2 && !p.RestartRequired && p.Files == 0 && p.Bytes == 0 {
			return nil
		}
	}
	return authority.Error{Code: authority.InvalidContract}
}

type projectionRepairImage struct {
	State       string
	Fingerprint string
	Files       int
	Bytes       int64
}

// Planning holds the source turn and intent mutex only for a finite read. It
// never opens a missing metadata namespace for creation or synchronizes it.
// Apply must independently revalidate this same intake under joined ownership.
func (c *Coordinator) PlanProjectionRepair(ctx context.Context, project authority.UUID) (ProjectionRepairPlan, error) {
	var zero ProjectionRepairPlan
	s, call, release, err := c.begin(ctx, project)
	if err != nil {
		return zero, err
	}
	defer release()
	p := c.projects[project]
	// Immutable resource bindings are installed before coordinator publication.
	if p.indexJobs == nil {
		code := p.indexJobsError
		if code == "" {
			code = IndexJobsUncertain
		}
		return zero, Error{code}
	}
	q := p.indexJobs
	if err := q.lock(call); err != nil {
		return zero, err
	}
	defer q.mu.Unlock()
	status, err := s.Status(call)
	if err != nil {
		return zero, err
	}
	if status.Readiness != "ready" {
		return zero, Error{ProjectionUncertain}
	}
	plan := ProjectionRepairPlan{Schema: 1, ProjectID: project, Sequence: status.AuthorityGeneration, LedgerState: "missing", LedgerGeneration: q.state.Generation, State: "not_needed", Action: "none"}
	if q.state.Generation > 0 {
		plan.LedgerState = "ready"
	}
	// Terminal ledger state is not sufficient if the actual attempt's lifetime
	// has not joined. The admission mutex protects that owned worker identity.
	if !c.mu.TryLock() {
		return zero, authority.Error{Code: authority.Conflict}
	}
	active := p.activeIndex != ""
	closing := c.closing || c.lifetime.Err() != nil || !c.jobsStarted
	c.mu.Unlock()
	if closing {
		return zero, authority.Error{Code: authority.Unavailable}
	}
	uncertain := false
	for _, r := range q.state.Jobs {
		active = active || r.State == "queued" || r.State == "running" || r.State == "cancelling"
		uncertain = uncertain || r.Code == "outcome_uncertain" || r.Code == string(ProjectionUncertain)
	}
	image := projectionRepairImage{}
	if uncertain {
		plan.State, plan.Blocker = "blocked", "index_uncertain"
	} else if active {
		plan.State, plan.Blocker = "blocked", "index_active"
	} else {
		if p.projection != nil {
			if !p.projection.mu.TryLock() {
				return zero, authority.Error{Code: authority.Conflict}
			}
			defer p.projection.mu.Unlock()
		}
		image, err = inspectProjectionRepair(call, s)
		if err != nil {
			return zero, err
		}
		plan.Files, plan.Bytes = image.Files, image.Bytes
		if image.State == "corrupt" {
			plan.State, plan.Action, plan.RestartRequired = "ready", "quarantine", true
		} else if p.projection == nil || p.projection.fence != "" || p.projection.verifyExpected() != nil {
			// Well-formed current bytes do not clear this owner's publication
			// or physical-generation fence. Qualify a new owner, not a purge.
			return zero, Error{ProjectionUncertain}
		}
	}
	// Validate source binding and intent again after transient filesystem reads.
	// A plan is not permission to ignore later source/ledger/catalog changes.
	if err := q.verifyExpected(); err != nil {
		return zero, err
	}
	if err := s.verifyPhysicalBinding(); err != nil {
		return zero, err
	}
	if call.Err() != nil {
		return zero, call.Err()
	}
	guard, err := json.Marshal(struct {
		Domain                                               string
		Plan                                                 ProjectionRepairPlan
		Tracker                                              authority.UUID
		Manifest, Root, LedgerFile, LedgerDigest, Projection string
	}{"projection-repair-plan-v1", plan, s.instance, manifestHash(s.manifest), q.rootIdentity, q.fileIdentity, q.fileHash, image.Fingerprint})
	if err != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	plan.PlanID = hash(guard)
	if err := plan.Validate(); err != nil {
		return zero, err
	}
	return plan, nil
}
