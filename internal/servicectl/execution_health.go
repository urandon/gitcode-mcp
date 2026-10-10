package servicectl

import (
	"strings"
	"time"

	"gitcode-mcp/internal/observability"
)

// healthAuthorityRecord copies only bounded primitive authority fields while
// locks are held. Ref hashing, grouping and public projection happen afterwards.
// Neither private Error nor progress/source text is copied.
type healthAuthorityRecord struct {
	cache, repo, registration, source, job, kind, collection, signal, reason string
	at, lastSuccess, retry                                                   time.Time
}

func boundedHealthIdentity(r healthAuthorityRecord) bool {
	return len(r.cache)+len(r.repo)+len(r.registration)+len(r.source)+len(r.kind)+len(r.collection)+6 <= 4096
}

func healthFailureReason(class string) string {
	switch class {
	case "network_timeout", "provider_timeout":
		return "provider_timeout"
	case "network_unavailable", "provider_unavailable":
		return "provider_unavailable"
	case "auth_expired", "forbidden":
		return "provider_forbidden"
	case "rate_limited":
		return "provider_rate_limited"
	case "api_validation", "schema_decode", "partial_response":
		return "provider_invalid"
	case "cache_authority_unavailable", "cache_uuid_mismatch", "cache_schema_blocked":
		return "cache_authority"
	case "config_snapshot_invalid", "configuration_error", "identity_conflict", "identity_unresolved", "cache_clone_conflict":
		return "configuration_invalid"
	case "cache_inspection_timeout":
		return "cache_authority"
	case "rag_failed", "embedding_failed":
		return "embedding_failed"
	default:
		return "operation_failed"
	}
}
func healthCollection(collection string) string {
	switch collection {
	case "issues", "issue_comments", "wiki", "pulls", "pr_comments":
		return collection
	default:
		return "other"
	}
}

func healthRemoteCollection(remote string) string {
	switch remote {
	case "issue":
		return "issues"
	case "issue_comment":
		return "issue_comments"
	case "wiki":
		return "wiki"
	case "pull_request":
		return "pulls"
	case "pr_comment":
		return "pr_comments"
	default:
		return "other"
	}
}

// ExecutionHealth is a read-only, non-waiting projection of retained durable
// job/registry truth. It never uses observation storage/counters as authority.
// Missing or oversized evidence is not a healthy empty result.
func (m *MaintenanceManager) ExecutionHealth(at time.Time) observability.ExecutionHealth {
	if m == nil || m.jobs == nil {
		return observability.UnavailableExecutionHealth(at, "authority_missing")
	}
	if !m.mu.TryLock() {
		return observability.UnavailableExecutionHealth(at, "authority_busy")
	}
	if !m.jobs.mu.TryLock() {
		m.mu.Unlock()
		return observability.UnavailableExecutionHealth(at, "authority_busy")
	}
	records := make([]healthAuthorityRecord, 0, min(len(m.jobs.jobs)*2, observability.MaxHealthEvidence))
	limited := false
	visited := 0
	visit := func() bool {
		visited++
		if visited > observability.MaxHealthEvidence {
			limited = true
			return false
		}
		return true
	}
	add := func(record healthAuthorityRecord) {
		if len(records) >= observability.MaxHealthEvidence {
			limited = true
			return
		}
		records = append(records, record)
	}
	active := make(map[retryCohort]bool)
	for _, job := range m.jobs.jobs {
		if !visit() {
			break
		}
		if job == nil {
			continue
		}
		base := healthAuthorityRecord{cache: job.CacheUUID, repo: job.RepoID, registration: job.RegistrationID, source: job.SourceRegistrationID, job: job.ID, kind: metricWorkKind(job.Type), collection: "other", at: job.UpdatedAt, reason: healthFailureReason(job.ErrorClass)}
		if jobActiveStatus(job.Status) && boundedHealthIdentity(base) {
			active[retryCohort{job.CacheUUID, job.RepoID, job.Type, job.SourceRegistrationID}] = true
		}
		if job.Status == JobStatusCancelled || job.Status == JobStatusSuperseded {
			continue
		}
		if job.FinishedAt != nil {
			base.at = *job.FinishedAt
		}
		if base.at.IsZero() {
			base.at = job.CreatedAt
		}
		switch job.Status {
		case JobStatusFailed:
			base.signal = "failed"
		case JobStatusInterrupted:
			base.signal, base.reason = "interrupted", "interrupted"
		case JobStatusSucceeded:
			base.signal, base.reason = "succeeded", "succeeded"
			if job.Type == SyncJobType && (job.SyncHealth == SyncHealthPartial || job.SyncHealth == SyncHealthFailed) {
				base.signal, base.reason = "failed", healthFailureReason(job.ErrorClass)
			}
		}
		terminalRepresented := false
		if job.Type == SyncJobType && len(job.SyncCollections) > 0 {
			for _, collection := range job.SyncCollections {
				if !visit() {
					break
				}
				r := base
				r.collection = healthCollection(collection.Collection)
				if !collection.UpdatedAt.IsZero() {
					r.at = collection.UpdatedAt
				}
				if collection.LastSuccessAt != nil {
					r.lastSuccess = *collection.LastSuccessAt
				}
				r.reason = healthFailureReason(collection.ErrorClass)
				switch collection.Outcome {
				case SyncCollectionSuccess, SyncCollectionNoChange:
					r.signal, r.reason = "succeeded", "succeeded"
				case SyncCollectionPermanentFailure, SyncCollectionPartial:
					r.signal = "failed"
				case SyncCollectionRetryScheduled:
					if jobActiveStatus(job.Status) {
						r.signal, r.reason = "retrying", "retry_wait"
						if collection.RetryAfter != nil {
							r.retry = *collection.RetryAfter
						}
					} else if job.Status == JobStatusInterrupted {
						r.signal, r.reason = "interrupted", "interrupted"
					} else {
						r.signal = "failed"
					}
				default:
					continue
				}
				add(r)
				terminalRepresented = terminalRepresented || (r.signal == base.signal && r.reason == base.reason && r.at.Equal(base.at) && job.ErrorClass != "" && collection.ErrorClass == job.ErrorClass)
			}
		}
		// Collection snapshots are not the entire execution authority. A
		// later stage rejection can fail an otherwise successful partial run.
		if stage := job.SyncStage; job.Type == SyncJobType && job.Status == JobStatusFailed && stage != nil && stage.Phase == SyncStageRejected {
			base.collection = healthCollection(stage.Collection)
			add(base)
		} else if base.signal != "" && (base.signal == "succeeded" || !terminalRepresented) {
			// Whole-job success also recovers preceding whole-job faults;
			// only identical terminal evidence deduplicates a failure. An
			// older collection problem cannot stand for a later job fault.
			add(base)
		}
		if stage := job.SyncStage; jobActiveStatus(job.Status) && stage != nil && stage.Phase == SyncStageWaitingCommit {
			r := base
			r.collection = healthCollection(stage.Collection)
			r.signal, r.reason = "waiting_writer", "writer_wait"
			r.at = stage.UpdatedAt
			r.retry = stage.RetryAfter
			add(r)
		}
	}
	for registration, entry := range m.entries {
		if limited || !visit() {
			break
		}
		if entry == nil {
			continue
		}
		base := healthAuthorityRecord{cache: entry.CacheUUID, repo: entry.RepoID, registration: registration, kind: "sync", collection: "other"}
		for _, frontier := range entry.Frontiers {
			if !visit() {
				break
			}
			r := base
			r.collection = healthRemoteCollection(frontier.RemoteType)
			success := frontier.LastSuccessAt
			// Legacy complete, error-free frontier metadata is a confirmed
			// success. A mere queued/pending timestamp is not proof of work.
			if success.IsZero() && frontier.Status == "complete" && frontier.LastErrorClass == "" {
				success = frontier.UpdatedAt
			}
			if !success.IsZero() {
				r.at = success
				r.signal, r.reason = "succeeded", "succeeded"
				add(r)
			}
			if frontier.Status == "degraded" || frontier.LastErrorClass != "" {
				r.at, r.lastSuccess = frontier.UpdatedAt, success
				r.signal, r.reason = "failed", healthFailureReason(frontier.LastErrorClass)
				add(r)
			}
		}
		// Automatic authority blocking disables admission, not the fault.
		// Explicit user-disable uses state "disabled" and adds no failure.
		switch entry.State {
		case "cache_schema_blocked", "config_snapshot_invalid", "identity_conflict", "identity_unresolved", "cache_inspection_timeout", "cache_clone_conflict", "degraded":
			r := base
			r.kind = "maintenance"
			r.at = entry.LastReconciledAt
			r.signal = "failed"
			r.reason = healthFailureReason(entry.LastErrorClass)
			add(r)
		}
		if !entry.Enabled {
			continue
		}
		stageRecord := func(stage MaintenanceStageState, kind, jobType, source string) {
			identity := base
			identity.source = source
			if stage.Status == JobStatusCancelled || stage.Status == JobStatusSuperseded {
				return
			}
			running := boundedHealthIdentity(identity) && active[retryCohort{entry.CacheUUID, entry.RepoID, jobType, source}]
			r := base
			r.kind, r.source, r.job, r.at = kind, source, stage.LastJobID, stage.UpdatedAt
			r.reason = healthFailureReason(stage.LastErrorClass)
			if stage.Status == JobStatusSucceeded && stage.ConsecutiveFailures == 0 {
				r.signal, r.reason = "succeeded", "succeeded"
			} else if !stage.RetryAfter.IsZero() {
				if running {
					r.signal = "failed"
				} else {
					r.signal = "retrying"
					r.retry = stage.RetryAfter
				}
			} else if stage.Status == JobStatusInterrupted {
				r.signal, r.reason = "interrupted", "interrupted"
			} else if stage.ConsecutiveFailures > 0 || stage.Status == JobStatusFailed {
				r.signal = "failed"
			}
			if r.signal != "" {
				add(r)
			}
		}
		if entry.Policy.SyncEnabled {
			stageRecord(entry.SyncStage, "sync", SyncJobType, "")
		}
		if entry.Policy.RAGEnabled {
			stageRecord(entry.RAGStage, "rag", RAGIndexJobType, "")
		}
		if len(m.sources[registration]) > 0 {
			for _, source := range m.sources[registration] {
				if !visit() {
					break
				}
				if source != nil {
					cancelled, available := m.retrySourceCancelledLocked(registration, entry, source.State.SourceRegistrationID)
					if !available {
						limited = true
						break
					}
					if !cancelled {
						stageRecord(source.State.Stage, "repository_docs", RepositoryDocsIndexJobType, source.State.SourceRegistrationID)
					}
				}
			}
		} else if source := entry.RepositoryDocs; source != nil {
			cancelled, available := m.retrySourceCancelledLocked(registration, entry, source.SourceRegistrationID)
			if !available {
				limited = true
			} else if !cancelled {
				stageRecord(source.Stage, "repository_docs", RepositoryDocsIndexJobType, source.SourceRegistrationID)
			}
		}
	}
	m.jobs.mu.Unlock()
	m.mu.Unlock()
	if limited {
		return observability.UnavailableExecutionHealth(at, "evidence_limit")
	}
	evidence := make([]observability.HealthEvidence, 0, len(records))
	for _, r := range records {
		var cohort observability.Ref
		if boundedHealthIdentity(r) {
			key := strings.Join([]string{r.kind, r.cache, r.repo, r.registration, r.source, r.collection}, "\x00")
			cohort = observability.KnownRef("correlation", key)
		}
		evidence = append(evidence, observability.HealthEvidence{Cohort: cohort, Origin: r.kind, Collection: r.collection, Signal: r.signal, Reason: r.reason, At: r.at, LastSuccess: r.lastSuccess, RetryAfter: r.retry,
			Refs: observability.Context{Job: observability.KnownRef("job", r.job), Registration: observability.KnownRef("registration", r.registration), Cache: observability.KnownRef("cache", r.cache), Repo: observability.KnownRef("repo", r.repo)}})
	}
	return observability.ProjectExecutionHealth(at, evidence)
}
