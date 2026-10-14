package localauthority

import (
	"context"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/observability"
)

type canonicalObservationKey struct{}

// One synchronous owner call, with no callback or retained private content.
// State 0 is no new publication; 1 is possible; 2 is proof-confirmed. The
// coordinator publishes the capture only after Store and project-turn release.
type canonicalObservation struct {
	project  authority.UUID
	document bool
	state    uint8
}

func captureCanonical(ctx context.Context, project authority.UUID, document bool, confirmed bool) {
	capture, ok := ctx.Value(canonicalObservationKey{}).(*canonicalObservation)
	if !ok || capture.project != project || capture.document != document {
		return
	}
	// database/sql refuses Commit before entering the driver when the owning
	// transaction context is already cancelled. That known pre-driver rollback
	// is not an uncertain publication. Cancellation after this check remains
	// conservatively possible; confirmed proof must survive lost caller context.
	if !confirmed && ctx.Err() != nil {
		return
	}
	if confirmed {
		capture.state = 2
	} else if capture.state == 0 {
		capture.state = 1
	}
}

func (c *canonicalObservation) publish(observer observability.LocalProjectObserver) {
	switch c.state {
	case 1:
		observer.UnconfirmedCommit()
	case 2:
		if c.document {
			observer.DocumentCommitted()
		} else {
			observer.SourceCommitted()
		}
	}
}
