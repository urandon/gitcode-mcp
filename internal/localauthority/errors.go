// Package localauthority owns durable local tracker state, never a cache or
// provider. Runtime enrollment and transport rollout are separate cohorts.
package localauthority

import "context"

type Code string

const (
	ReadOnly                       Code = "authority_read_only"
	Corrupt                        Code = "authority_corrupt"
	DurabilityUnavailable          Code = "authority_durability_unavailable"
	Capacity                       Code = "authority_capacity"
	StorageUnavailable             Code = "authority_storage_unavailable"
	CommitUncertain                Code = "authority_commit_uncertain"
	InitializationPending          Code = "authority_initialization_pending"
	RefConflict                    Code = "authority_ref_conflict"
	KnowledgeMigrationRequired     Code = "authority_knowledge_migration_required"
	KnowledgeSetupRequired         Code = "authority_knowledge_setup_required"
	KnowledgeInitializationPending Code = "authority_knowledge_initialization_pending"
	DocumentPending                Code = "authority_document_pending"
	DocumentRecoveryRequired       Code = "authority_document_recovery_required"
	KnowledgeCorrupt               Code = "authority_knowledge_corrupt"
	JobsCorrupt                    Code = "authority_recovery_jobs_corrupt"
	JobsUncertain                  Code = "authority_recovery_jobs_uncertain"
	JobNotFound                    Code = "authority_recovery_job_not_found"
	JobRetryLimit                  Code = "authority_recovery_job_retry_limit"
	ProjectionCorrupt              Code = "authority_projection_corrupt"
	ProjectionUncertain            Code = "authority_projection_uncertain"
	IndexJobsCorrupt               Code = "authority_index_jobs_corrupt"
	IndexJobsUncertain             Code = "authority_index_jobs_uncertain"
)

// Error is deliberately content-free; SQL, paths, keys and payloads are private.
type Error struct{ Code Code }

func (e Error) DiagnosticCode() string { return string(e.Code) }
func (e Error) Error() string {
	switch e.Code {
	case ReadOnly:
		return "local authority is read-only"
	case Corrupt:
		return "local authority integrity check failed"
	case DurabilityUnavailable:
		return "local authority durability is unavailable"
	case Capacity:
		return "local authority operation exceeds capacity"
	case CommitUncertain:
		return "local authority commit acknowledgement is uncertain; retain the original caller key"
	case InitializationPending:
		return "local authority initialization is pending; retry the original initialization intent"
	case RefConflict:
		return "knowledge reference changed; read current state before a new intent"
	case KnowledgeMigrationRequired:
		return "knowledge authority requires an explicit verified schema migration"
	case KnowledgeSetupRequired:
		return "knowledge authority requires explicit setup"
	case KnowledgeInitializationPending:
		return "knowledge initialization is pending; retain the original setup intent"
	case DocumentPending:
		return "another document operation is pending; resolve its original intent"
	case DocumentRecoveryRequired:
		return "document operation requires recovery; retain the original caller key"
	case KnowledgeCorrupt:
		return "knowledge authority integrity check failed; preserve original operation evidence"
	case JobsCorrupt:
		return "local recovery job metadata is invalid; preserve it for explicit recovery"
	case JobsUncertain:
		return "local recovery job publication is uncertain; verified owner reopen is required"
	case JobNotFound:
		return "local recovery job is not retained"
	case JobRetryLimit:
		return "local recovery job reached its bounded attempt limit"
	case ProjectionCorrupt:
		return "derived projection is invalid; canonical authority remains separate"
	case ProjectionUncertain:
		return "derived projection publication is uncertain; verified owner reopen is required"
	case IndexJobsCorrupt:
		return "local index job metadata is invalid; preserve it for explicit recovery"
	case IndexJobsUncertain:
		return "local index job publication is uncertain; verified owner reopen is required"
	default:
		return "local authority storage is unavailable"
	}
}

func storageError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return Error{StorageUnavailable}
}
