package service

import (
	"context"

	"gitcode-mcp/internal/observability"
)

// confirmedSyncObservation contains only fixed collection identity and the
// number of source graphs included in a confirmed atomic commit. It is volatile
// evidence, never a receipt or execution authority. The owner defers finish
// before acquiring its writer lease so recording runs after lease release.
type confirmedSyncObservation struct {
	ctx        context.Context
	confirmed  bool
	metrics    *observability.Metrics
	collection string
	records    uint64
}

func newConfirmedSyncObservation(ctx context.Context, collection string) *confirmedSyncObservation {
	metrics := observability.MetricsFromContext(ctx)
	if metrics == nil && !observability.HasObservation(ctx) {
		return nil
	}
	switch collection {
	case "issues", "issue_comments", "wiki", "pulls", "pr_comments":
	default:
		collection = "other"
	}
	if metrics != nil {
		metrics.AttachOrigin("sync")
	}
	return &confirmedSyncObservation{ctx: ctx, metrics: metrics, collection: collection}
}

func (o *confirmedSyncObservation) confirm(records int) {
	if o != nil && records >= 0 {
		o.confirmed = true
		o.records += uint64(records)
	}
}

func (o *confirmedSyncObservation) finish() {
	if o != nil {
		if o.metrics != nil {
			o.metrics.Add("sync_committed", map[string]string{"collection": o.collection}, o.records)
		}
		if o.confirmed {
			observability.ConfirmSyncCommit(o.ctx, o.collection)
		}
	}
}
