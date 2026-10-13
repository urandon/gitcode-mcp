package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"unicode/utf8"
)

// ValidActorID preserves the store's asserted-attribution contract. It does
// not authenticate identity; only qualified kernel peers authenticate tenancy.
func ValidActorID(id string) bool {
	if !utf8.ValidString(id) || len(id) < 1 || len(id) > 128 || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// AttributedMutationRequest is separate from the unchanged legacy Mutate DTO.
// Document effects require the independent versioned document contract.
type AttributedMutationRequest struct {
	ActorID string          `json:"actor_id"`
	Request MutationRequest `json:"request"`
}

func (r AttributedMutationRequest) Validate() error {
	if !ValidActorID(r.ActorID) {
		return Error{InvalidContract}
	}
	if err := r.Request.Validate(); err != nil {
		return err
	}
	if strings.HasPrefix(r.Request.Operation, "document.") {
		return Error{Unsupported}
	}
	return nil
}

type DocumentMutationCall struct {
	ActorID string                  `json:"actor_id"`
	Request DocumentMutationRequest `json:"request"`
}

func (r DocumentMutationCall) Validate() error {
	if !ValidActorID(r.ActorID) {
		return Error{InvalidContract}
	}
	return r.Request.Validate()
}

type ProjectRequest struct {
	ProjectID UUID `json:"project_id"`
}

func (r ProjectRequest) Validate() error {
	if !r.ProjectID.Valid() {
		return Error{InvalidContract}
	}
	return nil
}

// OriginalOperationRequest is a protected read, never consent to replay or
// settle an operation. Raw keys must not enter response or observation data.
type OriginalOperationRequest struct {
	ProjectID      UUID   `json:"project_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (r OriginalOperationRequest) Validate() error {
	return (WriteIntent{ProjectID: r.ProjectID, WriteMode: "local", IdempotencyKey: r.IdempotencyKey}).Validate(true)
}

func (r OriginalOperationRequest) KeyHash() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(string(r.ProjectID) + "\x00" + r.IdempotencyKey))
	return hex.EncodeToString(digest[:]), nil
}

type CleanupDocumentRequest struct {
	ProjectID      UUID   `json:"project_id"`
	IdempotencyKey string `json:"idempotency_key"`
	WriteMode      string `json:"write_mode"`
}

func (r CleanupDocumentRequest) Original() OriginalOperationRequest {
	return OriginalOperationRequest{ProjectID: r.ProjectID, IdempotencyKey: r.IdempotencyKey}
}

func (r CleanupDocumentRequest) Validate() error {
	if r.WriteMode != "local" {
		return Error{InvalidContract}
	}
	return r.Original().Validate()
}

type AuditRequest struct {
	ProjectID UUID   `json:"project_id"`
	After     uint64 `json:"after"`
	Limit     int    `json:"limit"`
}

func (r AuditRequest) Validate() error {
	if !r.ProjectID.Valid() || r.After > math.MaxInt64 || r.Limit < 1 || r.Limit > 100 {
		return Error{InvalidContract}
	}
	return nil
}
