package authority

import (
	"encoding/json"
	"math"
)

// DocumentContractSchema is independent of manifest, database and transport
// versions. These DTOs do not activate a transport or grant write admission.
const DocumentContractSchema = 1
const OrderedOSBarriersV1 = "ordered_os_barriers_v1"

// EmptyKnowledgeRef is an explicit absent-ref expectation, not an inferred CAS.
const EmptyKnowledgeRef = "0000000000000000000000000000000000000000000000000000000000000000"

type DocumentWriteIntent struct {
	ProjectID        UUID   `json:"project_id"`
	WriteMode        string `json:"write_mode"`
	IdempotencyKey   string `json:"idempotency_key"`
	ExpectedRefOID   string `json:"expected_ref_oid"`
	EntityID         UUID   `json:"entity_id,omitempty"`
	ExpectedRevision uint64 `json:"expected_revision,omitempty"`
}

type DocumentMutationRequest struct {
	Schema    int                 `json:"schema"`
	Intent    DocumentWriteIntent `json:"intent"`
	Operation string              `json:"operation"`
	Payload   json.RawMessage     `json:"payload"`
}

func (r DocumentMutationRequest) Validate() error {
	if r.Schema != DocumentContractSchema {
		return Error{Unsupported}
	}
	switch r.Operation {
	case "document.create", "document.update", "document.tombstone":
	default:
		return Error{Unsupported}
	}
	if !digestValid(r.Intent.ExpectedRefOID, 64) || r.Intent.ExpectedRevision > math.MaxInt64 || r.Operation != "document.create" && r.Intent.ExpectedRefOID == EmptyKnowledgeRef {
		return Error{InvalidContract}
	}
	// Reuse only the payload and identity grammar, never generic write admission.
	return (MutationRequest{Intent: WriteIntent{
		ProjectID: r.Intent.ProjectID, WriteMode: r.Intent.WriteMode,
		IdempotencyKey: r.Intent.IdempotencyKey, EntityID: r.Intent.EntityID,
		ExpectedRevision: r.Intent.ExpectedRevision,
	}, Operation: r.Operation, Payload: r.Payload}).Validate()
}

func (r *DocumentMutationRequest) UnmarshalJSON(data []byte) error {
	type wire DocumentMutationRequest
	var decoded wire
	if err := DecodeJSON(data, &decoded); err != nil {
		return err
	}
	// omitempty cannot distinguish absent fields from explicit empty placeholders.
	// Create reserves its own identity; callers may not provide either field.
	if decoded.Operation == "document.create" {
		var fields struct {
			Intent map[string]json.RawMessage `json:"intent"`
		}
		if json.Unmarshal(data, &fields) != nil {
			return Error{InvalidContract}
		}
		for _, key := range []string{"entity_id", "expected_revision"} {
			if _, present := fields.Intent[key]; present {
				return Error{InvalidContract}
			}
		}
	}
	next := DocumentMutationRequest(decoded)
	if err := next.Validate(); err != nil {
		return err
	}
	*r = next
	return nil
}

type DocumentTombstoneEvidence struct {
	Citation    Citation `json:"citation"`
	OperationID UUID     `json:"operation_id"`
	CommitOID   string   `json:"commit_oid"`
	ParentOID   string   `json:"parent_oid"`
}

// DocumentOutcome keeps deletion evidence distinct from a live byte locator.
type DocumentOutcome struct {
	State     string                     `json:"state"`
	Locator   *DocumentLocator           `json:"locator,omitempty"`
	Tombstone *DocumentTombstoneEvidence `json:"tombstone,omitempty"`
}

func nonzeroDocumentOID(s string) bool {
	return digestValid(s, 64) && s != EmptyKnowledgeRef
}

func (o DocumentOutcome) Validate() error {
	switch o.State {
	case "live":
		if o.Locator == nil || o.Tombstone != nil || o.Locator.Validate() != nil || !nonzeroDocumentOID(o.Locator.CommitOID) || !nonzeroDocumentOID(o.Locator.BlobOID) {
			return Error{InvalidContract}
		}
	case "tombstone":
		if o.Locator != nil || o.Tombstone == nil {
			return Error{InvalidContract}
		}
		t := o.Tombstone
		if t.Citation.Validate() != nil || t.Citation.Kind != Document || t.Citation.Revision < 2 || !t.OperationID.Valid() || !nonzeroDocumentOID(t.CommitOID) || !nonzeroDocumentOID(t.ParentOID) || t.CommitOID == t.ParentOID {
			return Error{InvalidContract}
		}
	default:
		return Error{InvalidContract}
	}
	return nil
}

func (o *DocumentOutcome) UnmarshalJSON(data []byte) error {
	type wire DocumentOutcome
	var decoded wire
	if err := DecodeJSON(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return Error{InvalidContract}
	}
	// Even a null forbidden arm is not a valid union representation.
	forbidden := "tombstone"
	if decoded.State == "tombstone" {
		forbidden = "locator"
	}
	if _, present := fields[forbidden]; present {
		return Error{InvalidContract}
	}
	next := DocumentOutcome(decoded)
	if err := next.Validate(); err != nil {
		return err
	}
	*o = next
	return nil
}

type DocumentMutationResult struct {
	Schema     int             `json:"schema"`
	Receipt    MutationReceipt `json:"receipt"`
	Durability string          `json:"durability"`
	Result     DocumentOutcome `json:"result"`
}

func (r DocumentMutationResult) Validate() error {
	if r.Schema != DocumentContractSchema {
		return Error{Unsupported}
	}
	if r.Receipt.Validate() != nil || r.Receipt.Citation.Kind != Document || r.Durability != OrderedOSBarriersV1 || r.Result.Validate() != nil {
		return Error{InvalidContract}
	}
	if r.Result.State == "live" {
		if r.Result.Locator.Citation != r.Receipt.Citation {
			return Error{InvalidContract}
		}
	} else if r.Result.Tombstone.Citation != r.Receipt.Citation || r.Result.Tombstone.OperationID != r.Receipt.OperationID {
		return Error{InvalidContract}
	}
	return nil
}

func (r *DocumentMutationResult) UnmarshalJSON(data []byte) error {
	type wire DocumentMutationResult
	var decoded wire
	if err := DecodeJSON(data, &decoded); err != nil {
		return err
	}
	next := DocumentMutationResult(decoded)
	if err := next.Validate(); err != nil {
		return err
	}
	*r = next
	return nil
}
