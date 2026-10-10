package servicectl

import "gitcode-mcp/internal/observability"

// retryCohort is private comparison identity, not a metric label or public DTO.
type retryCohort struct{ cache, repo, kind, source string }

// ScheduledRetryProbe observes one coherent in-memory projection of durable
// jobs and registry state. Both locks are try-only; it performs no I/O, timer
// wait, callback, admission or recovery. Due windows remain scheduled until
// work is admitted. Ordinary periodic polling is not a retry.
func (m *MaintenanceManager) ScheduledRetryProbe() observability.ProbeValue {
	if m == nil || m.jobs == nil || !m.mu.TryLock() {
		return observability.Unavailable()
	}
	defer m.mu.Unlock()
	if !m.jobs.mu.TryLock() {
		return observability.Unavailable()
	}
	defer m.jobs.mu.Unlock()
	active := make(map[retryCohort]bool)
	var scheduled int
	visited := 0
	visit := func() bool { visited++; return visited <= observability.MaxHealthEvidence }
	for _, job := range m.jobs.jobs {
		if !visit() {
			return observability.Unavailable()
		}
		if job == nil || !jobActiveStatus(job.Status) {
			continue
		}
		if !boundedHealthIdentity(healthAuthorityRecord{cache: job.CacheUUID, repo: job.RepoID, source: job.SourceRegistrationID, kind: job.Type}) {
			return observability.Unavailable()
		}
		active[retryCohort{job.CacheUUID, job.RepoID, job.Type, job.SourceRegistrationID}] = true
		if job.Type != SyncJobType || job.Status == JobStatusCancelling {
			continue
		}
		waiting := make(map[string]bool, 5)
		for _, collection := range job.SyncCollections {
			if !visit() {
				return observability.Unavailable()
			}
			if collection.Outcome == SyncCollectionRetryScheduled && collection.RetryAfter != nil && !collection.RetryAfter.IsZero() {
				waiting[healthCollection(collection.Collection)] = true
			}
		}
		if stage := job.SyncStage; stage != nil && stage.Phase == SyncStageWaitingCommit && !stage.RetryAfter.IsZero() {
			waiting[healthCollection(stage.Collection)] = true
		}
		scheduled += len(waiting)
	}
	for registration, entry := range m.entries {
		if !visit() {
			return observability.Unavailable()
		}
		if entry == nil || !entry.Enabled {
			continue
		}
		if !boundedHealthIdentity(healthAuthorityRecord{cache: entry.CacheUUID, repo: entry.RepoID}) {
			return observability.Unavailable()
		}
		cohort := retryCohort{cache: entry.CacheUUID, repo: entry.RepoID}
		cohort.kind = SyncJobType
		if entry.Policy.SyncEnabled && !entry.SyncStage.RetryAfter.IsZero() && !active[cohort] {
			scheduled++
		}
		cohort.kind = RAGIndexJobType
		if entry.Policy.RAGEnabled && !entry.RAGStage.RetryAfter.IsZero() && !active[cohort] {
			scheduled++
		}
		cohort.kind = RepositoryDocsIndexJobType
		// Canonical source registrations own the retry windows. The legacy
		// single-source summary is a compatibility projection, not another job.
		if len(m.sources[registration]) > 0 {
			for _, source := range m.sources[registration] {
				if !visit() {
					return observability.Unavailable()
				}
				if source == nil {
					continue
				}
				cohort.source = source.State.SourceRegistrationID
				if len(cohort.source) > 4096 {
					return observability.Unavailable()
				}
				if !source.State.Stage.RetryAfter.IsZero() && !active[cohort] {
					scheduled++
				}
			}
		} else if source := entry.RepositoryDocs; source != nil {
			cohort.source = source.SourceRegistrationID
			if len(cohort.source) > 4096 {
				return observability.Unavailable()
			}
			if !source.Stage.RetryAfter.IsZero() && !active[cohort] {
				scheduled++
			}
		}
	}
	return observability.Available(float64(scheduled))
}
