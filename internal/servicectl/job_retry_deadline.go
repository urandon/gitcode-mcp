package servicectl

import (
	"strings"
	"time"
)

// jobRetryDeadline projects retained retry evidence onto one exact deadline.
// Exact stage/collection timestamps are authoritative. Legacy progress events
// may retain a duration instead; anchor those to the job update that persisted
// the event so callers can distinguish active backoff from historical context.
func jobRetryDeadline(job Job, collection string) (time.Time, bool) {
	collection = strings.TrimSpace(collection)
	var exactDeadline time.Time
	observeExact := func(candidate time.Time) {
		if !candidate.IsZero() && candidate.After(exactDeadline) {
			exactDeadline = candidate.UTC()
		}
	}

	if job.SyncStage != nil && (collection == "" || job.SyncStage.Collection == collection) {
		observeExact(job.SyncStage.RetryAfter)
	}
	for _, state := range job.SyncCollections {
		if collection != "" && state.Collection != collection {
			continue
		}
		if state.RetryAfter != nil {
			observeExact(*state.RetryAfter)
		}
	}

	var legacyDeadline time.Time
	observeLegacy := func(candidate time.Time) {
		if !candidate.IsZero() && candidate.After(legacyDeadline) {
			legacyDeadline = candidate.UTC()
		}
	}
	anchor := job.UpdatedAt
	if anchor.IsZero() {
		anchor = job.CreatedAt
	}
	for _, event := range job.Progress {
		if collection != "" && event.Collection != collection {
			continue
		}
		value := strings.TrimSpace(event.RetryAfter)
		if value == "" {
			continue
		}
		if exact, err := time.Parse(time.RFC3339, value); err == nil {
			observeExact(exact)
			continue
		}
		if delay, err := time.ParseDuration(value); err == nil && !anchor.IsZero() {
			observeLegacy(anchor.Add(delay))
		}
	}
	if !exactDeadline.IsZero() {
		return exactDeadline, true
	}
	return legacyDeadline, !legacyDeadline.IsZero()
}
