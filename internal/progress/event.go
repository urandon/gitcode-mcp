package progress

// Event is the transport-neutral progress record shared by sync, RAG, and
// daemon job status. Keeping it outside those packages prevents lifecycle
// packages from depending on the service orchestration layer.
type Event struct {
	Type                string `json:"type,omitempty"`
	Phase               string `json:"phase,omitempty"`
	Collection          string `json:"collection,omitempty"`
	Page                int    `json:"page,omitempty"`
	RecordsListed       int    `json:"records_listed,omitempty"`
	RecordsFetched      int    `json:"records_fetched,omitempty"`
	RecordsInserted     int    `json:"records_inserted,omitempty"`
	RecordsUpdated      int    `json:"records_updated,omitempty"`
	RecordsSkipped      int    `json:"records_skipped,omitempty"`
	RecordsDeferred     int    `json:"records_deferred,omitempty"`
	RecordsFailed       int    `json:"records_failed,omitempty"`
	RecordsDeleted      int    `json:"records_deleted,omitempty"`
	RevisionSetsDeleted int    `json:"revision_sets_deleted,omitempty"`
	ChunksDeleted       int    `json:"chunks_deleted,omitempty"`
	VectorsDeleted      int    `json:"vectors_deleted,omitempty"`
	BytesBefore         int64  `json:"bytes_before,omitempty"`
	BytesAfter          int64  `json:"bytes_after,omitempty"`
	LastSeenCursor      string `json:"last_seen_cursor,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	RetryAfter          string `json:"retry_after,omitempty"`
	ResumeAt            string `json:"resume_at,omitempty"`
	Attempt             int    `json:"attempt,omitempty"`
	Concurrency         int    `json:"concurrency,omitempty"`
	RateLimitRPS        string `json:"rate_limit_rps,omitempty"`
	RateLimitBurst      int    `json:"rate_limit_burst,omitempty"`
	RateLimitState      string `json:"rate_limit_state,omitempty"`
	Message             string `json:"message,omitempty"`
}
