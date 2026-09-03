package service

import "gitcode-mcp/internal/progress"

type SyncDiagnostic string

const (
	SyncDiagnosticCancelled SyncDiagnostic = "sync_cancelled"
	SyncDiagnosticTimeout   SyncDiagnostic = "sync_timeout"
	SyncDiagnosticEmptyWiki SyncDiagnostic = "empty_wiki"
)

type ProgressEvent = progress.Event

type SyncBounds struct {
	MaxPages     int                  `json:"-"`
	MaxRecords   int                  `json:"-"`
	MaxBytes     int64                `json:"-"`
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
