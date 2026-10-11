package servicectl

import "context"

// Pending resolution evidence is attempt-local, not part of the context carrier.
// Registry rollback cannot restore that carrier; only confirmation promotes it.
// Pending + already retained observations share the same 128 logical budget.
type maintenanceResolutionObservation struct {
	buffer       *maintenanceObservationBuffer
	previous     string
	before       maintenanceObservationState
	captures     [maxMaintenanceObservations]maintenanceObservationCapture
	limit, count int
	lost         uint64
}

func newMaintenanceResolutionObservation(ctx context.Context, predecessor *MaintenanceEntry) *maintenanceResolutionObservation {
	buffer, _ := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
	if buffer == nil || buffer.collector == nil || predecessor == nil {
		return nil
	}
	return &maintenanceResolutionObservation{buffer: buffer, previous: predecessor.RegistrationID, before: maintenanceObservedIdentityState(predecessor), limit: len(buffer.captures) - buffer.count}
}

func maintenanceObservedIdentityState(entry *MaintenanceEntry) maintenanceObservationState {
	if entry == nil {
		return maintenanceObservationState{state: "unknown"}
	}
	switch entry.State {
	case "identity_conflict", "cache_clone_conflict", "identity_unresolved", "config_snapshot_invalid", "cache_inspection_timeout":
		return maintenanceObservationState{state: entry.State, failure: maintenanceObservedFailure(entry.LastErrorClass)}
	}
	return maintenanceObservedState(entry)
}

func (p *maintenanceResolutionObservation) record(entry *MaintenanceEntry) {
	if p == nil {
		return
	}
	after := maintenanceObservedIdentityState(entry)
	if p.before == after {
		return
	}
	predecessorValid := p.before.state == "identity_conflict" || p.before.state == "cache_clone_conflict" || p.before.state == "identity_unresolved"
	if !predecessorValid || after.state == "unknown" || entry == nil || p.previous == "" || len(p.previous) > 4096 || entry.RegistrationID == "" || len(entry.RegistrationID) > 4096 || entry.CacheUUID == "" || len(entry.CacheUUID) > 4096 || entry.RepoID == "" || len(entry.RepoID) > 4096 || p.count == p.limit {
		p.lost++
		return
	}
	p.captures[p.count] = maintenanceObservationCapture{identity: jobObservationIdentity{cache: entry.CacheUUID, registration: entry.RegistrationID, repo: entry.RepoID}, scope: "identity_resolution", previousRegistration: p.previous, before: p.before, after: after}
	p.count++
}

func (p *maintenanceResolutionObservation) confirm() {
	if p == nil {
		return
	}
	b := p.buffer
	if p.count > 0 {
		b.resolutions++ // at most 128 retained operation groups in this carrier
		for i := 0; i < p.count; i++ {
			if b.count == len(b.captures) {
				b.lost++
				continue
			}
			capture := p.captures[i]
			capture.resolution = b.resolutions
			b.captures[b.count] = capture
			b.count++
		}
	}
	b.lost += p.lost
	p.count, p.lost = 0, 0
}
