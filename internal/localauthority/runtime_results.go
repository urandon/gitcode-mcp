package localauthority

import (
	"math"
	"time"

	"gitcode-mcp/internal/authority"
)

// DocumentOperationResult binds an observation/cleanup to the original caller
// key without modifying the immutable L3 operation state or receipt.
type DocumentOperationResult struct {
	Schema             int                    `json:"schema"`
	IdempotencyKeyHash string                 `json:"idempotency_key_hash"`
	State              DocumentOperationState `json:"state"`
}

func (r DocumentOperationResult) Validate() error {
	if r.Schema != 1 || !validDigest(r.IdempotencyKeyHash) || r.State.Validate() != nil {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func (r DocumentOperationResult) Match(request authority.OriginalOperationRequest) error {
	want, err := request.KeyHash()
	if err != nil || r.Validate() != nil || r.State.ProjectID != request.ProjectID || r.IdempotencyKeyHash != want {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

type AuditResult struct {
	ProjectID authority.UUID `json:"project_id"`
	Entries   []AuditEntry   `json:"entries"`
}

func canonicalRecordedAt(at string) bool {
	if len(at) < 1 || len(at) > 64 {
		return false
	}
	stamp, err := time.Parse(time.RFC3339Nano, at)
	return err == nil && stamp.UTC().Format(time.RFC3339Nano) == at
}

func (a AuditEntry) Validate() error {
	if a.Sequence < 1 || a.Sequence > math.MaxInt64 || a.Revision < 1 || a.Revision > a.Sequence || !a.OperationID.Valid() || !a.EntityID.Valid() || !validDigest(a.KeyHash) || !validDigest(a.IntentHash) || !validDigest(a.ActorHash) || !canonicalRecordedAt(a.RecordedAt) {
		return authority.Error{Code: authority.InvalidContract}
	}
	known := false
	for _, contract := range authority.OperationContracts() {
		if contract.Operation == a.Operation {
			if contract.CAS == (a.Revision == 1) {
				return authority.Error{Code: authority.InvalidContract}
			}
			known = true
		}
	}
	if !known {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func (r AuditResult) Validate() error {
	if !r.ProjectID.Valid() || r.Entries == nil || len(r.Entries) > 100 {
		return authority.Error{Code: authority.InvalidContract}
	}
	var last uint64
	seen := make(map[authority.UUID]bool, len(r.Entries))
	for _, entry := range r.Entries {
		if entry.Validate() != nil || entry.Sequence <= last || seen[entry.OperationID] {
			return authority.Error{Code: authority.InvalidContract}
		}
		last = entry.Sequence
		seen[entry.OperationID] = true
	}
	return nil
}

func (r AuditResult) Match(request authority.AuditRequest) error {
	if request.Validate() != nil || r.Validate() != nil || r.ProjectID != request.ProjectID || len(r.Entries) > request.Limit || len(r.Entries) > 0 && r.Entries[0].Sequence <= request.After {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func (r RevisionMetadata) Validate() error {
	if r.Citation.Validate() != nil || !authority.ValidActorID(r.ActorID) || !canonicalRecordedAt(r.RecordedAt) {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}
