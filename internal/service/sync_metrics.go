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
	metrics    *observability.Metrics
	collection string
	records    uint64
}

func newConfirmedSyncObservation(ctx context.Context, collection string) *confirmedSyncObservation {
	metrics := observability.MetricsFromContext(ctx)
	if metrics == nil {
		return nil
	}
	switch collection {
	case "issues", "issue_comments", "wiki", "pulls", "pr_comments":
	default:
		collection = "other"
	}
	metrics.AttachOrigin("sync")
	return &confirmedSyncObservation{metrics: metrics, collection: collection}
}

func (o *confirmedSyncObservation) confirm(records int) {
	if o != nil && records >= 0 {
		o.records += uint64(records)
	}
}

func (o *confirmedSyncObservation) finish() {
	if o != nil {
		o.metrics.Add("sync_committed", map[string]string{"collection": o.collection}, o.records)
	}
}
