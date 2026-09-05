package servicectl

import (
	"strings"
	"time"
)

// jobRetryDeadline projects retained retry evidence onto one exact deadline.
// Exact stage/collection timestamps are authoritative. Legacy progress events
// may retain a duration instead; anchor those to the job update that persisted
// the event so callers can distinguish active backoff from historical context.
// Precedence is per collection: one collection's exact evidence must not hide
// another collection's legacy fallback from a whole-job retry check.
func jobRetryDeadline(job Job, collection string) (time.Time, bool) {
	collection = strings.TrimSpace(collection)
	type deadlineEvidence struct {
		exact  time.Time
		legacy time.Time
	}
	buckets := map[string]deadlineEvidence{}
	observe := func(scope string, candidate time.Time, exact bool) {
		scope = strings.TrimSpace(scope)
		if candidate.IsZero() || (collection != "" && scope != collection) {
			return
		}
		evidence := buckets[scope]
		if exact && candidate.After(evidence.exact) {
			evidence.exact = candidate.UTC()
		}
		if !exact && candidate.After(evidence.legacy) {
			evidence.legacy = candidate.UTC()
		}
		buckets[scope] = evidence
	}

	if job.SyncStage != nil {
		observe(job.SyncStage.Collection, job.SyncStage.RetryAfter, true)
	}
	for _, state := range job.SyncCollections {
		if state.RetryAfter != nil {
			observe(state.Collection, *state.RetryAfter, true)
		}
	}

	anchor := job.UpdatedAt
	if anchor.IsZero() {
		anchor = job.CreatedAt
	}
	for _, event := range job.Progress {
		value := strings.TrimSpace(event.RetryAfter)
		if value == "" {
			continue
		}
		if exact, err := time.Parse(time.RFC3339, value); err == nil {
			observe(event.Collection, exact, true)
			continue
		}
		if delay, err := time.ParseDuration(value); err == nil && !anchor.IsZero() {
			observe(event.Collection, anchor.Add(delay), false)
		}
	}
	var deadline time.Time
	for _, evidence := range buckets {
		candidate := evidence.exact
		if candidate.IsZero() {
			candidate = evidence.legacy
		}
		if candidate.After(deadline) {
			deadline = candidate
		}
	}
	return deadline, !deadline.IsZero()
}
