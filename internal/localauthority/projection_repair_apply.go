package localauthority

import (
	"context"
	"gitcode-mcp/internal/authority"
)

// Apply is registered in the owner's joined lifetime and source turn. It is
// not a purge, index admission, source mutation or permission to start a host.
func (c *Coordinator) ApplyProjectionRepair(ctx context.Context, req ProjectionRepairRequest, actor Actor) (ProjectionRepairView, error) {
	var zero ProjectionRepairView
	if req.Validate() != nil || !actor.valid() {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	_, call, release, err := c.begin(ctx, req.ProjectID)
	if err != nil {
		return zero, err
	}
	defer release()
	if !c.mu.TryLock() {
		return zero, authority.Error{Code: authority.Conflict}
	}
	available := c.jobsStarted && !c.closing && c.lifetime.Err() == nil
	c.mu.Unlock()
	if !available {
		return zero, authority.Error{Code: authority.Unavailable}
	}
	p := c.projects[req.ProjectID]
	if p.repair == nil {
		return zero, Error{RepairUncertain}
	}
	q := p.repair
	if err = q.lock(call); err != nil {
		return zero, err
	}
	defer q.mu.Unlock()
	if _, _, err = q.inventory(call); err != nil {
		return zero, err
	}
	key, intent := repairIntentHashes(req)
	index, replay := -1, false
	for i, r := range q.state.Records {
		if r.KeyHash != key {
			continue
		}
		if r.IntentHash != intent || r.ActorHash != jobActorHash(req.ProjectID, actor) {
			return zero, authority.Error{Code: authority.Conflict}
		}
		index, replay = i, true
		break
	}
	if index >= 0 && q.state.Records[index].State == "confirmed" {
		return repairView(q.state.Records[index], true), nil
	}
	// A confirmed historical replay grants no index work and is qualified by
	// its own retained intent/quarantine above. Only pending/new effects need
	// the unrelated index-ledger admission; corruption cannot erase a receipt.
	if p.indexJobs == nil {
		return zero, Error{RepairUncertain}
	}
	if err = p.indexJobs.lock(call); err != nil {
		return zero, err
	}
	defer p.indexJobs.mu.Unlock()
	if index < 0 {
		if q.pending() || q.restartRequired {
			return zero, Error{RepairUncertain}
		}
		if len(q.state.Records) >= maxProjectionRepairs {
			return zero, Error{Capacity}
		}
		var image projectionRepairImage
		plan, err := c.planProjectionRepairOwned(call, p, &image)
		if err != nil {
			return zero, err
		}
		if plan.State != "ready" || plan.PlanID != req.PlanID {
			return zero, authority.Error{Code: authority.Conflict}
		}
		id, err := authority.NewUUID()
		if err != nil {
			return zero, err
		}
		r := projectionRepairRecord{ID: id, KeyHash: key, IntentHash: intent, ActorHash: jobActorHash(req.ProjectID, actor), Plan: plan, RootIdentity: q.rootIdentity, ProjectionIdentity: image.DirectoryIdentity, ProjectionFingerprint: image.Fingerprint, IndexIdentity: p.indexJobs.fileIdentity, IndexDigest: p.indexJobs.fileHash, State: "prepared", Generation: 1}
		next := q.state
		if next.Generation == 0 {
			next = projectionRepairSnapshot{Schema: 1, ProjectID: req.ProjectID, TrackerInstance: p.store.instance, ManifestHash: manifestHash(p.store.manifest)}
		}
		next.Generation++
		next.Records = append(append([]projectionRepairRecord(nil), next.Records...), r)
		if err = q.publish(call, next); err != nil {
			return zero, err
		}
		index = len(next.Records) - 1
	}
	if p.projection != nil {
		if !p.projection.mu.TryLock() {
			return zero, authority.Error{Code: authority.Conflict}
		}
		defer p.projection.mu.Unlock()
	}
	if err = c.resumeProjectionRepair(call, p, index); err != nil {
		return zero, err
	}
	return repairView(q.state.Records[index], replay), nil
}

func advanceRepair(ctx context.Context, q *projectionRepairStore, index int, phase string) error {
	next := q.state
	next.Generation++
	next.Records = append([]projectionRepairRecord(nil), next.Records...)
	next.Records[index].State = phase
	next.Records[index].Generation++
	return q.publish(ctx, next)
}

func (c *Coordinator) resumeProjectionRepair(ctx context.Context, p *coordinatedProject, index int) (err error) {
	q := p.repair
	r := q.state.Records[index]
	effectPossible := false
	// Capture the function result, not a block-local inspection error. Every
	// possible-effect exit (including later proof/phase/ack failure) fences the
	// live owner; only qualified reopen can reconcile the retained operation.
	defer func() {
		if err != nil && effectPossible {
			q.fence = RepairUncertain
		}
	}()
	if err = q.verifyExpected(); err != nil {
		return err
	}
	if p.indexJobs.state.Generation != r.Plan.LedgerGeneration || p.indexJobs.fileIdentity != r.IndexIdentity || p.indexJobs.fileHash != r.IndexDigest || p.indexJobs.verifyExpected() != nil {
		return Error{RepairUncertain}
	}
	// Existing intent never authorizes a new selection after a changed source or
	// index namespace. Canonical content may advance; the derived inode/intake is
	// still exactly the original one, not a new catalog chosen during replay.
	if r.RootIdentity != q.rootIdentity || p.store.verifyPhysicalBinding() != nil {
		return Error{RepairCorrupt}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	quarantine, _, err := openRepairSubdirectory(q.directory, repairQuarantineName(r.ID), false)
	if err != nil {
		return err
	}
	if quarantine != nil {
		_ = quarantine.Close()
	}
	if r.State == "prepared" && quarantine == nil {
		image, err := inspectProjectionRepair(ctx, p.store)
		if err != nil {
			return err
		}
		if image.DirectoryIdentity != r.ProjectionIdentity || image.Fingerprint != r.ProjectionFingerprint || image.Files != r.Plan.Files || image.Bytes != r.Plan.Bytes {
			return Error{RepairCorrupt}
		}
		current, _, err := openProjectionNamespace(q.root, false)
		if err != nil || current == nil {
			return Error{RepairCorrupt}
		}
		defer current.Close()
		heldID, err := physicalIdentity(current)
		if err != nil || heldID != r.ProjectionIdentity {
			return Error{RepairCorrupt}
		}
		owned := false
		if p.projection != nil && p.projection.directory != nil {
			id, e := physicalIdentity(p.projection.directory)
			owned = e == nil && id == heldID
		}
		if !owned {
			if err = lockExclusive(current); err != nil {
				return err
			}
		}
		if err = q.inject(ctx, "repair_before_quarantine"); err != nil {
			return err
		}
		after, err := inspectProjectionRepair(ctx, p.store)
		if err != nil {
			return err
		}
		if after != image || q.verifyExpected() != nil || p.indexJobs.verifyExpected() != nil {
			return Error{RepairCorrupt}
		}
		// Once a move is attempted, caller cancellation cannot prove rollback.
		// Preserve pending intent and fence the live owner until verified reopen.
		q.restartRequired = true
		effectPossible = true
		if p.projection != nil {
			p.projection.fence = ProjectionUncertain
		}
		if err = moveProjectionToQuarantine(q.root, q.directory, r.ID); err != nil {
			return err
		}
		if err = q.inject(ctx, "repair_quarantine_rename"); err != nil {
			return err
		}
	} else {
		current, _, err := openProjectionNamespace(q.root, false)
		if err != nil {
			return err
		}
		if current != nil {
			_ = current.Close()
			return Error{RepairCorrupt}
		}
		q.restartRequired = true
		effectPossible = true
		if p.projection != nil {
			p.projection.fence = ProjectionUncertain
		}
	}
	if err = q.verifyQuarantine(ctx, r, true); err != nil {
		return err
	}
	if err = q.inject(ctx, "repair_quarantine_readback"); err != nil {
		return err
	}
	if r.State == "prepared" {
		if err = advanceRepair(ctx, q, index, "quarantined"); err != nil {
			return err
		}
	}
	if err = q.verifyQuarantine(ctx, q.state.Records[index], true); err != nil {
		return err
	}
	if err = advanceRepair(ctx, q, index, "confirmed"); err != nil {
		return err
	}
	return q.inject(ctx, "repair_receipt_acknowledgement")
}

func (c *Coordinator) ProjectionRepair(ctx context.Context, project, id authority.UUID) (ProjectionRepairView, error) {
	var zero ProjectionRepairView
	if !id.Valid() {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	_, call, release, err := c.begin(ctx, project)
	if err != nil {
		return zero, err
	}
	defer release()
	q := c.projects[project].repair
	if q == nil {
		return zero, Error{RepairUncertain}
	}
	if err = q.lock(call); err != nil {
		return zero, err
	}
	defer q.mu.Unlock()
	if _, _, err = q.inventory(call); err != nil {
		return zero, err
	}
	for _, r := range q.state.Records {
		if r.ID == id {
			return repairView(r, false), nil
		}
	}
	return zero, authority.Error{Code: authority.NoAuthority}
}
