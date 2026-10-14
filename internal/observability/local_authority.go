package observability

import (
	"context"
	"strconv"
)

// LocalProjectObserver retains one trusted concrete collector or memory relay
// and an opaque identity resolved from admitted owner state. No caller refs or
// callbacks are inherited; binding never starts a collector or probes storage.
type LocalProjectObserver struct {
	collector *Collector
	relay     *LocalProjectRelay
	project   Ref
}

func BindLocalProject(ctx context.Context, admittedProjectID string) LocalProjectObserver {
	binding, ok := ctx.Value(observationContextKey{}).(observationBinding)
	if ok && binding.collector != nil {
		return LocalProjectObserver{collector: binding.collector, project: KnownRef("project", admittedProjectID)}
	}
	relay, _ := ctx.Value(localRelayContextKey{}).(*LocalProjectRelay)
	ref := KnownRef("project", admittedProjectID)
	if relay != nil && relay.projects[ref.value] {
		return LocalProjectObserver{relay: relay, project: ref}
	}
	return LocalProjectObserver{}
}

func (o LocalProjectObserver) SourceCommitted()   { o.committed("authority_commit") }
func (o LocalProjectObserver) DocumentCommitted() { o.committed("document_commit") }

func (o LocalProjectObserver) Bound() bool {
	return (o.collector != nil || o.relay != nil) && o.project.value != ""
}

// Retained distinguishes an already owned parent collector from the child's
// memory relay. Only the former can request retained native delivery.
func (o LocalProjectObserver) Retained() bool     { return o.collector != nil && o.project.value != "" }
func (o LocalProjectObserver) ProjectRef() string { return o.project.value }

func localAuthorityCode(code Code) bool {
	return code == LocalAuthorityCommitted || code == LocalAuthorityPhaseChanged
}

func localAuthorityPhase(code Code, phase, outcome string) bool {
	if code == LocalAuthorityCommitted {
		return (phase == "authority_commit" || phase == "document_commit") && outcome == "committed"
	}
	if code != LocalAuthorityPhaseChanged {
		return false
	}
	return ValidLocalProjectPhase(phase, outcome)
}

// ValidLocalProjectPhase is shared by owner-side bounded capture and retained
// event admission/replay. It never admits arbitrary provider or caller labels.
func ValidLocalProjectPhase(phase, outcome string) bool {
	switch phase {
	case "index_job", "knowledge_recovery":
		switch outcome {
		case "queued", "running", "cancelling", "cancelled", "succeeded", "failed", "interrupted":
			return true
		}
	case "projection_repair":
		return outcome == "prepared" || outcome == "quarantined" || outcome == "confirmed"
	}
	return false
}

// PhaseChanged consumes only the finite proof boundary captured by an admitted
// owner. It does not interpret errors or assert current resource health.
func (o LocalProjectObserver) PhaseChanged(phase, outcome string) {
	o.emit(LocalAuthorityPhaseChanged, phase, outcome)
}

// A publication outcome could not be qualified, or its bounded observation
// could not be retained. Coverage is partial; this never infers job health or
// emits a success event/fabricated throughput.
func (o LocalProjectObserver) UnconfirmedCommit() {
	if o.Bound() {
		if o.collector != nil {
			o.collector.UnconfirmedExecution()
		} else {
			o.relay.omit()
		}
	}
}

// UnconfirmedCommits folds one bounded native loss delta without per-event
// iteration. Relay senders cannot use this to manufacture parent authority.
func (o LocalProjectObserver) UnconfirmedCommits(count uint64) {
	if o.Retained() && count <= LocalProjectRelayMaxLoss {
		o.collector.UnconfirmedExecutions(count)
	}
}

func (o LocalProjectObserver) committed(phase string) {
	o.emit(LocalAuthorityCommitted, phase, "committed")
}

func (o LocalProjectObserver) emit(code Code, phase, outcome string) {
	if !o.Bound() {
		return
	}
	if !localAuthorityPhase(code, phase, outcome) {
		return
	}
	if o.relay != nil {
		o.relay.emit(LocalProjectTransition{o.project.value, code, phase, outcome})
		return
	}
	correlation := KnownRef("correlation", o.collector.boot+"\x00authority\x00"+strconv.FormatUint(o.collector.authorityTransitions.Add(1), 10))
	o.collector.Emit(code, Context{Project: o.project, Correlation: correlation, ExecutionDetails: ExecutionDetails{Phase: phase, Outcome: outcome}})
}
