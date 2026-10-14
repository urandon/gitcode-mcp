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

// The owner attempted canonical publication but could not qualify its outcome.
// Coverage is partial; no success event or fabricated throughput is emitted.
func (o LocalProjectObserver) UnconfirmedCommit() {
	if o.collector != nil && o.project.value != "" {
		o.collector.UnconfirmedExecution()
	}
}

func (o LocalProjectObserver) committed(phase string) {
	if o.collector == nil || o.project.value == "" {
		return
	}
	correlation := KnownRef("correlation", o.collector.boot+"\x00authority\x00"+strconv.FormatUint(o.collector.authorityTransitions.Add(1), 10))
	o.collector.Emit(LocalAuthorityCommitted, Context{Project: o.project, Correlation: correlation, ExecutionDetails: ExecutionDetails{Phase: phase, Outcome: "committed"}})
}
