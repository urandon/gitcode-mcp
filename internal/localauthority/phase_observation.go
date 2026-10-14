package localauthority

import (
	"context"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/observability"
)

const maxPhaseObservations = 8

type phaseObservationKey struct{}

type phasePublication struct {
	phase, outcome string
	confirmed      bool
}

// One synchronous owned call/turn. Only finite phase/outcome values enter this
// fixed-size buffer: no source, actor, key, job UUID, callback or error body.
// Possible publication is captured at the real rename boundary, proof after
// existing barriers/readback. Emission happens after source/ledger ownership
// release, while still registered in the joined owner lifetime.
type phaseObservation struct {
	project    authority.UUID
	canonical  *canonicalObservation
	boundaries [maxPhaseObservations]phasePublication
	count      uint8
	overflow   uint8 // saturating lower bound; flush remains bounded
}

func observePhases(ctx context.Context, p *coordinatedProject) (context.Context, *phaseObservation) {
	if !p.observation.Bound() {
		return ctx, nil
	}
	b := &phaseObservation{project: p.store.manifest.ID}
	if canonical, ok := ctx.Value(canonicalObservationKey{}).(*canonicalObservation); ok && canonical.project == b.project {
		b.canonical = canonical
	}
	return context.WithValue(ctx, phaseObservationKey{}, b), b
}

func capturePhasePublication(ctx context.Context, project authority.UUID, phase, outcome string) *phasePublication {
	b, ok := ctx.Value(phaseObservationKey{}).(*phaseObservation)
	if !ok || b.project != project || !observability.ValidLocalProjectPhase(phase, outcome) {
		return nil
	}
	// Existing publisher checks reject pre-publication cancellation. Once this
	// real rename boundary is reached, later cancellation cannot prove rollback:
	// Renameat does not consult the context. Keep possibility even if cancelled.
	if int(b.count) == len(b.boundaries) {
		if b.overflow < 255 {
			b.overflow++
		}
		return nil
	}
	p := &b.boundaries[b.count]
	*p = phasePublication{phase: phase, outcome: outcome}
	b.count++
	return p
}

// Snapshot transition validation has already established exactly one changed
// record. Empty namespace provisioning is not a job/repair state transition.
func indexPhaseOutcome(before, next indexJobSnapshot) string {
	for _, r := range next.Jobs {
		found := false
		for _, old := range before.Jobs {
			if old.Selection.BuildID == r.Selection.BuildID {
				found = true
				if old.Generation != r.Generation {
					return r.State
				}
				break
			}
		}
		if !found {
			return r.State
		}
	}
	return ""
}

func recoveryPhaseOutcome(before, next recoveryJobSnapshot) string {
	for _, r := range next.Jobs {
		found := false
		for _, old := range before.Jobs {
			if old.View.JobID == r.View.JobID {
				found = true
				if old.View.Generation != r.View.Generation {
					return r.View.State
				}
				break
			}
		}
		if !found {
			return r.View.State
		}
	}
	return ""
}

func repairPhaseOutcome(before, next projectionRepairSnapshot) string {
	for _, r := range next.Records {
		found := false
		for _, old := range before.Records {
			if old.ID == r.ID {
				found = true
				if old.Generation != r.Generation {
					return r.State
				}
				break
			}
		}
		if !found {
			return r.State
		}
	}
	return ""
}

func (p *phasePublication) confirm() {
	if p != nil {
		p.confirmed = true
	}
}

func (b *phaseObservation) publish(observer observability.LocalProjectObserver) {
	if b == nil {
		return
	}
	n, lost := b.count, b.overflow
	canonical := b.canonical
	b.canonical = nil
	b.count, b.overflow = 0, 0
	if canonical != nil {
		canonical.publish(observer)
	}
	for i := uint8(0); i < n; i++ {
		p := b.boundaries[i]
		b.boundaries[i] = phasePublication{}
		if p.confirmed {
			observer.PhaseChanged(p.phase, p.outcome)
		} else {
			observer.UnconfirmedCommit()
		}
	}
	for ; lost > 0; lost-- {
		observer.UnconfirmedCommit()
	}
}
