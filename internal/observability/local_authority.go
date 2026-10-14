package observability

import (
	"context"
	"strconv"
)

// LocalProjectObserver retains only a trusted concrete collector and an opaque
// identity resolved from admitted owner state. No caller refs or callbacks are
// inherited, and constructing it never starts a collector or probes storage.
type LocalProjectObserver struct {
	collector *Collector
	project   Ref
}

func BindLocalProject(ctx context.Context, admittedProjectID string) LocalProjectObserver {
	binding, ok := ctx.Value(observationContextKey{}).(observationBinding)
	if !ok || binding.collector == nil {
		return LocalProjectObserver{}
	}
	return LocalProjectObserver{collector: binding.collector, project: KnownRef("project", admittedProjectID)}
}

func (o LocalProjectObserver) SourceCommitted()   { o.committed("authority_commit") }
func (o LocalProjectObserver) DocumentCommitted() { o.committed("document_commit") }

func (o LocalProjectObserver) Bound() bool { return o.collector != nil && o.project.value != "" }

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
		o.collector.UnconfirmedExecution()
	}
}

func (o LocalProjectObserver) committed(phase string) {
	o.emit(LocalAuthorityCommitted, phase, "committed")
}

func (o LocalProjectObserver) emit(code Code, phase, outcome string) {
	if !o.Bound() {
		return
	}
	correlation := KnownRef("correlation", o.collector.boot+"\x00authority\x00"+strconv.FormatUint(o.collector.authorityTransitions.Add(1), 10))
	o.collector.Emit(code, Context{Project: o.project, Correlation: correlation, ExecutionDetails: ExecutionDetails{Phase: phase, Outcome: outcome}})
}
