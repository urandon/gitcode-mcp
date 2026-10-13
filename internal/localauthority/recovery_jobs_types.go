package localauthority

import (
	"encoding/json"
	"math"
	"reflect"
	"time"

	"gitcode-mcp/internal/authority"
)

const (
	MaxRecoveryJobs             = 128
	MaxRecoveryJobAttempts      = 8
	MaxRecoveryJobSnapshotBytes = 512 << 10
	MaxRecoveryJobStages        = 32
	MaxRecoveryJobStageBytes    = 16 << 20
	MaxRecoveryJobPageBytes     = 64 << 10
)

type RecoveryJobLookup struct {
	ProjectID authority.UUID `json:"project_id"`
	JobID     authority.UUID `json:"job_id"`
}

func (r RecoveryJobLookup) Validate() error {
	if !r.ProjectID.Valid() || !r.JobID.Valid() {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

// The canonical operation UUID is the recovery idempotency identity. It cannot
// reserve another document operation, substitute a source key, or retarget Git.
type RecoveryJobRequest struct {
	ProjectID          authority.UUID `json:"project_id"`
	OperationID        authority.UUID `json:"operation_id"`
	ObservedGeneration int            `json:"observed_generation"`
	WriteMode          string         `json:"write_mode"`
}

func (r RecoveryJobRequest) Validate() error {
	if !r.ProjectID.Valid() || !r.OperationID.Valid() || r.ObservedGeneration < 1 || r.ObservedGeneration > 3 || r.WriteMode != "local" {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

type RecoveryJobAction struct {
	ProjectID          authority.UUID `json:"project_id"`
	JobID              authority.UUID `json:"job_id"`
	ExpectedGeneration uint64         `json:"expected_generation"`
	WriteMode          string         `json:"write_mode"`
}

func (r RecoveryJobAction) Validate() error {
	if !r.ProjectID.Valid() || !r.JobID.Valid() || r.ExpectedGeneration < 1 || r.ExpectedGeneration > math.MaxInt64 || r.WriteMode != "local" {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

// This is protected application state, never a telemetry payload. It contains
// no actor text, caller key, document text, locator path or provider body.
type RecoveryJobView struct {
	Schema                    int                        `json:"schema"`
	ProjectID                 authority.UUID             `json:"project_id"`
	JobID                     authority.UUID             `json:"job_id"`
	Type                      string                     `json:"type"`
	State                     string                     `json:"state"`
	Generation                uint64                     `json:"generation"`
	ObservedJournalGeneration int                        `json:"observed_journal_generation"`
	Attempts                  int                        `json:"attempts"`
	Reconciled                bool                       `json:"reconciled"`
	Code                      string                     `json:"code,omitempty"`
	CreatedAt                 string                     `json:"created_at"`
	UpdatedAt                 string                     `json:"updated_at"`
	Receipt                   *authority.MutationReceipt `json:"receipt,omitempty"`
}

func recoveryJobCode(code string) bool {
	switch code {
	case "", "cancelled", "interrupted", "outcome_uncertain", "operation_failed",
		string(ReadOnly), string(Corrupt), string(DurabilityUnavailable), string(Capacity),
		string(StorageUnavailable), string(CommitUncertain), string(KnowledgeMigrationRequired),
		string(KnowledgeSetupRequired), string(KnowledgeInitializationPending),
		string(DocumentPending), string(DocumentRecoveryRequired), string(KnowledgeCorrupt),
		string(JobRetryLimit), string(authority.NoAuthority), string(authority.Conflict),
		string(authority.PolicyDenied), string(authority.InvalidContract), "timeout":
		return true
	default:
		return false
	}
}

func canonicalJobTime(stamp string) bool {
	v, err := time.Parse(time.RFC3339Nano, stamp)
	return err == nil && !v.IsZero() && v.UTC().Format(time.RFC3339Nano) == stamp
}

func (v RecoveryJobView) Validate() error {
	if v.Schema != 1 || !v.ProjectID.Valid() || !v.JobID.Valid() || v.Type != "document_recovery" || v.Generation < 1 || v.Generation > math.MaxInt64 || v.ObservedJournalGeneration < 1 || v.ObservedJournalGeneration > 3 || v.Attempts < 0 || v.Attempts > MaxRecoveryJobAttempts || !recoveryJobCode(v.Code) || !canonicalJobTime(v.CreatedAt) || !canonicalJobTime(v.UpdatedAt) {
		return authority.Error{Code: authority.InvalidContract}
	}
	if v.State != "succeeded" && (v.Receipt != nil || v.Reconciled) {
		return authority.Error{Code: authority.InvalidContract}
	}
	if v.Generation < uint64(v.Attempts)+1 || v.State != "queued" && v.Generation < 2 || v.State == "failed" && v.Attempts < 1 {
		return authority.Error{Code: authority.InvalidContract}
	}
	switch v.State {
	case "queued":
		if v.Code != "" || v.Attempts >= MaxRecoveryJobAttempts {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "running", "cancelling":
		if v.Code != "" || v.Attempts < 1 {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "succeeded":
		if v.Code != "" || v.Receipt == nil || v.Receipt.Validate() != nil || v.Receipt.Replayed || v.Receipt.ProjectID != v.ProjectID || v.Receipt.OperationID != v.JobID || v.Receipt.Citation.Kind != authority.Document || v.Attempts == 0 && !v.Reconciled {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "cancelled":
		if v.Code != "cancelled" {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "failed", "interrupted":
		if v.Code == "" {
			return authority.Error{Code: authority.InvalidContract}
		}
	default:
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

type recoveryJobRecord struct {
	View               RecoveryJobView `json:"view"`
	IntentHash         string          `json:"intent_hash"`
	ActorHash          string          `json:"actor_hash"`
	ActionActorHash    string          `json:"action_actor_hash"`
	AdmittedGeneration uint64          `json:"admitted_generation"`
	CancelFrom         uint64          `json:"cancel_from"`
	RetryFrom          uint64          `json:"retry_from"`
}

func (r recoveryJobRecord) valid(snapshot uint64, project authority.UUID) bool {
	v := r.View
	if v.Validate() != nil || v.ProjectID != project || !validDigest(r.IntentHash) || !validDigest(r.ActorHash) || r.ActionActorHash != "" && !validDigest(r.ActionActorHash) || r.AdmittedGeneration < 1 || r.AdmittedGeneration > snapshot || r.CancelFrom >= v.Generation || r.RetryFrom >= v.Generation {
		return false
	}
	if v.Generation >= snapshot || v.Generation > snapshot-r.AdmittedGeneration+1 {
		return false
	}
	if (v.State == "cancelling" || v.State == "cancelled") && r.CancelFrom == 0 {
		return false
	}
	if r.CancelFrom > 0 && r.ActionActorHash == "" || r.RetryFrom > 0 && r.ActionActorHash == "" {
		return false
	}
	if v.Receipt != nil && (v.Receipt.Replayed || v.Receipt.IntentHash != r.IntentHash) {
		return false
	}
	return true
}

func (r recoveryJobRecord) selected() recoverySelection {
	return recoverySelection{project: r.View.ProjectID, operation: r.View.JobID, intent: r.IntentHash, generation: r.View.ObservedJournalGeneration}
}

func cloneRecoveryRecord(r recoveryJobRecord) recoveryJobRecord {
	if r.View.Receipt != nil {
		receipt := *r.View.Receipt
		r.View.Receipt = &receipt
	}
	return r
}

type recoveryJobSnapshot struct {
	Schema          int                 `json:"schema"`
	ProjectID       authority.UUID      `json:"project_id"`
	TrackerInstance authority.UUID      `json:"tracker_instance"`
	ManifestHash    string              `json:"manifest_hash"`
	Generation      uint64              `json:"generation"`
	Jobs            []recoveryJobRecord `json:"jobs"`
}

func (s recoveryJobSnapshot) valid() bool {
	if s.Schema != 1 || !s.ProjectID.Valid() || !s.TrackerInstance.Valid() || !validDigest(s.ManifestHash) || s.Generation < 1 || s.Generation > math.MaxInt64 || s.Jobs == nil || len(s.Jobs) > MaxRecoveryJobs {
		return false
	}
	if uint64(len(s.Jobs)) >= s.Generation {
		return false
	}
	var last authority.UUID
	active := 0
	for _, r := range s.Jobs {
		if !r.valid(s.Generation, s.ProjectID) || r.View.JobID <= last {
			return false
		}
		last = r.View.JobID
		if r.View.State == "queued" || r.View.State == "running" || r.View.State == "cancelling" {
			active++
		}
	}
	return active <= 1
}

type recoveryJobEnvelope struct {
	State  recoveryJobSnapshot `json:"state"`
	Digest string              `json:"digest"`
}

func (e recoveryJobEnvelope) Validate() error {
	body, err := json.Marshal(e.State)
	if err != nil || !e.State.valid() || e.Digest != hash(body) {
		return Error{JobsCorrupt}
	}
	return nil
}

// Publication may not erase unresolved intent or regress a retained job. The
// source operation remains immutable; only the closed job lifecycle can change.
func recoverySnapshotTransition(old, next recoveryJobSnapshot) bool {
	if old.Generation == 0 {
		return next.Generation == 1 && len(next.Jobs) == 0
	}
	for _, before := range old.Jobs {
		i, found := recoveryRecordIndex(next.Jobs, before.View.JobID)
		if !found {
			if before.View.State != "succeeded" || before.View.Receipt == nil {
				return false
			}
			continue
		}
		after := next.Jobs[i]
		if reflect.DeepEqual(before, after) {
			continue
		}
		if after.IntentHash != before.IntentHash || after.ActorHash != before.ActorHash || after.AdmittedGeneration != before.AdmittedGeneration || after.View.CreatedAt != before.View.CreatedAt || after.View.ObservedJournalGeneration != before.View.ObservedJournalGeneration || after.View.Generation != before.View.Generation+1 {
			return false
		}
		a, b := after.View.State, before.View.State
		attempt := false
		switch b {
		case "queued":
			attempt = a == "running"
			if !attempt && a != "cancelled" && a != "succeeded" && a != "interrupted" {
				return false
			}
		case "running":
			attempt = a == "running" // verified owner restart, before source effect
			if !attempt && a != "cancelling" && a != "succeeded" && a != "failed" && a != "interrupted" {
				return false
			}
		case "cancelling":
			if a != "succeeded" && a != "cancelled" && a != "interrupted" {
				return false
			}
		case "failed", "cancelled", "interrupted":
			if a != "queued" && a != "succeeded" {
				return false
			}
		default:
			return false // succeeded is immutable except proved-safe retirement
		}
		wantAttempts := before.View.Attempts
		if attempt {
			wantAttempts++
		}
		if after.View.Attempts != wantAttempts {
			return false
		}
		if a == "queued" {
			if after.RetryFrom != before.View.Generation || after.CancelFrom != 0 || after.ActionActorHash == "" {
				return false
			}
		} else if after.RetryFrom != before.RetryFrom {
			return false
		}
		if a == "cancelling" || b == "queued" && a == "cancelled" {
			if after.CancelFrom != before.View.Generation || after.ActionActorHash == "" {
				return false
			}
		} else if a != "queued" && after.CancelFrom != before.CancelFrom {
			return false
		}
		if a == "succeeded" && b != "running" && b != "cancelling" && !after.View.Reconciled {
			return false
		}
	}
	for _, r := range next.Jobs {
		if _, found := recoveryRecordIndex(old.Jobs, r.View.JobID); !found && (r.View.State != "queued" || r.View.Generation != 1 || r.View.Attempts != 0 || r.AdmittedGeneration != next.Generation || r.CancelFrom != 0 || r.RetryFrom != 0) {
			return false
		}
	}
	return true
}
