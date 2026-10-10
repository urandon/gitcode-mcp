package observability

import (
	"context"
	"sync/atomic"
)

type syncStageContextKey struct{}
type syncStageMark struct {
	refs    Context
	omitted bool
}

var sealedSyncStage = &syncStageMark{}

// SyncStageBuffer holds one confirmed journal transition during one commit
// turn. It contains only opaque identity and closed fields, never an envelope.
type SyncStageBuffer struct {
	binding observationBinding
	mark    atomic.Pointer[syncStageMark]
}

// DeferSyncStage must precede the committing journal write. Flush only after
// releasing the coordinator turn; an unconfirmed write leaves no mark to emit.
func DeferSyncStage(ctx context.Context) (context.Context, *SyncStageBuffer) {
	binding, ok := ctx.Value(observationContextKey{}).(observationBinding)
	if !ok {
		return ctx, nil
	}
	buffer := &SyncStageBuffer{binding: binding}
	return context.WithValue(ctx, syncStageContextKey{}, buffer), buffer
}

// ConfirmSyncStage accepts identity from the validated journal owner. It must
// agree with the existing execution scope; a mismatched record cannot rebind
// the caller. Confirmation outside a commit turn emits immediately.
func ConfirmSyncStage(ctx context.Context, identity Context, details ExecutionDetails) {
	binding, ok := ctx.Value(observationContextKey{}).(observationBinding)
	if !ok || identity.Job != binding.refs.Job || identity.Cache != binding.refs.Cache || identity.Registration != binding.refs.Registration || identity.Repo != binding.refs.Repo {
		return
	}
	refs := binding.refs
	refs.Stage, refs.ExecutionDetails = identity.Stage, details
	// Validate before marking so malformed evidence cannot consume the turn.
	e := Event{Code: SyncStageChanged, JobRef: refs.Job.value, CacheRef: refs.Cache.value, RegistrationRef: refs.Registration.value, RepoRef: refs.Repo.value, CorrelationRef: refs.Correlation.value, StageRef: refs.Stage.value, ExecutionDetails: details}
	if !e.validExecution() || !validRef(e.StageRef, "stage") {
		return
	}
	if buffer, deferred := ctx.Value(syncStageContextKey{}).(*SyncStageBuffer); deferred {
		if buffer.binding == binding {
			buffer.mark.CompareAndSwap(nil, &syncStageMark{refs: refs})
		}
		return
	}
	binding.collector.Emit(SyncStageChanged, refs)
}

// OmitSyncStage records a confirmed journal boundary that cannot be represented
// in the closed catalog. It never changes execution authority or fabricates an
// event. During a commit turn the loss is buffered just like a valid event.
func OmitSyncStage(ctx context.Context, identity Context) {
	binding, ok := ctx.Value(observationContextKey{}).(observationBinding)
	if !ok || identity.Job != binding.refs.Job || identity.Cache != binding.refs.Cache || identity.Registration != binding.refs.Registration || identity.Repo != binding.refs.Repo {
		return
	}
	// Validate scope independently of the unrepresentable ordinal. No raw
	// journal state is retained and malformed scope cannot poison coverage.
	refs := binding.refs
	for _, ref := range [...]struct{ value, kind string }{
		{refs.Job.value, "job"}, {refs.Cache.value, "cache"}, {refs.Registration.value, "registration"},
		{refs.Repo.value, "repo"}, {refs.Correlation.value, "correlation"}, {identity.Stage.value, "stage"},
	} {
		if ref.value == "" || !validRef(ref.value, ref.kind) {
			return
		}
	}
	if buffer, deferred := ctx.Value(syncStageContextKey{}).(*SyncStageBuffer); deferred {
		if buffer.binding == binding {
			buffer.mark.CompareAndSwap(nil, &syncStageMark{omitted: true})
		}
		return
	}
	binding.collector.UnconfirmedExecution()
}

func (b *SyncStageBuffer) Flush() {
	if b == nil {
		return
	}
	mark := b.mark.Swap(sealedSyncStage)
	if mark == nil || mark == sealedSyncStage {
		return
	}
	if mark.omitted {
		b.binding.collector.UnconfirmedExecution()
		return
	}
	b.binding.collector.Emit(SyncStageChanged, mark.refs)
}
