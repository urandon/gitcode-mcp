package observability

import (
	"context"
	"sync/atomic"
)

type syncCommitContextKey struct{}
type syncCommitMark struct{ collection string }

var sealedSyncCommit = &syncCommitMark{}

// SyncCommitBuffer holds at most one confirmed collection for one coordinator
// commit turn. It is volatile evidence, not an execution receipt or callback.
// Mark and Flush each use a single atomic operation; neither waits or retries.
type SyncCommitBuffer struct {
	binding observationBinding
	mark    atomic.Pointer[syncCommitMark]
}

// DeferSyncCommit lets the lease owner confirm before its caller releases the
// coordinator turn. The caller must Flush after that release, even on error.
func DeferSyncCommit(ctx context.Context) (context.Context, *SyncCommitBuffer) {
	binding, ok := ctx.Value(observationContextKey{}).(observationBinding)
	if !ok {
		return ctx, nil
	}
	buffer := &SyncCommitBuffer{binding: binding}
	return context.WithValue(ctx, syncCommitContextKey{}, buffer), buffer
}

func ConfirmSyncCommit(ctx context.Context, collection string) {
	if !executionCollection(collection) {
		return
	}
	binding, ok := ctx.Value(observationContextKey{}).(observationBinding)
	if !ok {
		return
	}
	if buffer, deferred := ctx.Value(syncCommitContextKey{}).(*SyncCommitBuffer); deferred {
		// A rebound context must never mark a different job's confirmation.
		if buffer.binding == binding {
			buffer.mark.CompareAndSwap(nil, &syncCommitMark{collection: collection})
		}
		return
	}
	EmitFromContext(ctx, SyncCommitted, ExecutionDetails{Phase: "commit", Collection: collection, Outcome: "succeeded"})
}

func (b *SyncCommitBuffer) Flush() {
	if b == nil {
		return
	}
	mark := b.mark.Swap(sealedSyncCommit)
	if mark == nil || mark == sealedSyncCommit {
		return
	}
	refs := b.binding.refs
	refs.ExecutionDetails = ExecutionDetails{Phase: "commit", Collection: mark.collection, Outcome: "succeeded"}
	b.binding.collector.Emit(SyncCommitted, refs)
}
