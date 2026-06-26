package service

type SyncDiagnostic string

const (
	SyncDiagnosticCancelled SyncDiagnostic = "sync_cancelled"
	SyncDiagnosticTimeout   SyncDiagnostic = "sync_timeout"
)

type ProgressEvent struct {
	Collection     string `json:"collection"`
	Page           int    `json:"page"`
	RecordsFetched int    `json:"records_fetched"`
	LastSeenCursor string `json:"last_seen_cursor,omitempty"`
}

type SyncBounds struct {
	MaxPages     int                  `json:"-"`
	MaxRecords   int                  `json:"-"`
	ProgressChan chan<- ProgressEvent `json:"-"`
}

func emitProgress(ch chan<- ProgressEvent, ev ProgressEvent) {
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default:
	}
}
