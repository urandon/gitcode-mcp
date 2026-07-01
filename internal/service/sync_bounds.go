package service

type SyncDiagnostic string

const (
	SyncDiagnosticCancelled SyncDiagnostic = "sync_cancelled"
	SyncDiagnosticTimeout   SyncDiagnostic = "sync_timeout"
	SyncDiagnosticEmptyWiki SyncDiagnostic = "empty_wiki"
)

type ProgressEvent struct {
	Type            string `json:"type,omitempty"`
	Phase           string `json:"phase,omitempty"`
	Collection      string `json:"collection,omitempty"`
	Page            int    `json:"page,omitempty"`
	RecordsListed   int    `json:"records_listed,omitempty"`
	RecordsFetched  int    `json:"records_fetched,omitempty"`
	RecordsInserted int    `json:"records_inserted,omitempty"`
	RecordsUpdated  int    `json:"records_updated,omitempty"`
	RecordsSkipped  int    `json:"records_skipped,omitempty"`
	RecordsFailed   int    `json:"records_failed,omitempty"`
	LastSeenCursor  string `json:"last_seen_cursor,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	RetryAfter      string `json:"retry_after,omitempty"`
	ResumeAt        string `json:"resume_at,omitempty"`
	Attempt         int    `json:"attempt,omitempty"`
	Concurrency     int    `json:"concurrency,omitempty"`
	RateLimitRPS    string `json:"rate_limit_rps,omitempty"`
	RateLimitBurst  int    `json:"rate_limit_burst,omitempty"`
	RateLimitState  string `json:"rate_limit_state,omitempty"`
	Message         string `json:"message,omitempty"`
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
