package localauthority

import (
	"math"
	"strings"

	"gitcode-mcp/internal/authority"
)

// Verification is pure shared domain logic. A transport parses a concrete
// success, then binds it to the full canonical caller intent and attribution;
// it must not accept another valid receipt merely because its UUID matches.
func VerifyMutationReceipt(req authority.MutationRequest, actor Actor, r authority.MutationReceipt) error {
	if !actor.valid() {
		return authority.Error{Code: authority.InvalidContract}
	}
	payload, err := canonicalPayload(req)
	if err != nil {
		return err
	}
	req.Payload = payload
	parts := strings.Split(req.Operation, ".")
	return verifyReceipt(r, req.Intent.ProjectID, req.Intent.IdempotencyKey, mutationFingerprint(req, actor.ID), authority.EntityKind(parts[0]), req.Intent.EntityID, req.Intent.ExpectedRevision, parts[1] == "create")
}

func VerifyDocumentReceipt(req authority.DocumentMutationRequest, actor Actor, r authority.DocumentMutationResult) error {
	intent, err := documentFingerprint(req, actor)
	if err != nil {
		return err
	}
	if r.Validate() != nil {
		return authority.Error{Code: authority.InvalidContract}
	}
	if req.Operation == "document.tombstone" {
		if r.Result.State != "tombstone" || r.Result.Tombstone.ParentOID != req.Intent.ExpectedRefOID {
			return authority.Error{Code: authority.InvalidContract}
		}
	} else {
		if r.Result.State != "live" {
			return authority.Error{Code: authority.InvalidContract}
		}
		var payload authority.DocumentPayload
		if authority.DecodeJSON(req.Payload, &payload) != nil {
			return authority.Error{Code: authority.InvalidContract}
		}
		markdown := []byte(payload.Markdown)
		blob, err := newKnowledgeObject("blob", markdown)
		if err != nil || r.Result.Locator.SHA256 != hash(markdown) || r.Result.Locator.ByteLength != int64(len(markdown)) || r.Result.Locator.BlobOID != blob.oid {
			return authority.Error{Code: authority.InvalidContract}
		}
	}
	return verifyReceipt(r.Receipt, req.Intent.ProjectID, req.Intent.IdempotencyKey, intent, authority.Document, req.Intent.EntityID, req.Intent.ExpectedRevision, req.Operation == "document.create")
}

func verifyReceipt(r authority.MutationReceipt, project authority.UUID, key, intent string, kind authority.EntityKind, entity authority.UUID, revision uint64, create bool) error {
	invalid := authority.Error{Code: authority.InvalidContract}
	if r.Validate() != nil || r.ProjectID != project || r.Citation.Kind != kind || r.ProjectSequence > math.MaxInt64 || r.Citation.Revision > math.MaxInt64 || revision > math.MaxInt64 || r.IntentHash != intent || r.IdempotencyKeyHash != hash([]byte(string(project)+"\x00"+key)) {
		return invalid
	}
	if create {
		if r.Citation.Revision != 1 {
			return invalid
		}
	} else if r.Citation.EntityID != entity || r.Citation.Revision != revision+1 {
		return invalid
	}
	return nil
}
