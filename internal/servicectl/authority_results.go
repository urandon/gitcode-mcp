package servicectl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

func authorityResultTarget(method string, params any) any {
	switch method {
	case "AuthorityV1.Negotiate":
		return &authority.Negotiation{}
	case "AuthorityV1.Read":
		if params.(authority.ReadRequest).Operation == "status" {
			return &authority.ProjectStatus{}
		}
		return &authority.ReadResult{}
	case "AuthorityV1.MutateAttributed", "AuthorityV1.MutationReceipt":
		return &authority.MutationReceipt{}
	case "AuthorityV1.MutateDocument":
		return &authority.DocumentMutationResult{}
	case "AuthorityV1.KnowledgeStatus":
		return &localauthority.KnowledgeState{}
	case "AuthorityV1.DocumentOperationStatus", "AuthorityV1.CleanupDocumentPin":
		return &localauthority.DocumentOperationResult{}
	case "AuthorityV1.RevisionMetadata":
		return &localauthority.RevisionMetadata{}
	case "AuthorityV1.Audit":
		return &localauthority.AuditResult{}
	}
	return nil
}

func authorityDestination(method string, params, result any) error {
	if result == nil {
		return nil
	}
	target := authorityResultTarget(method, params)
	value := reflect.ValueOf(result)
	if target == nil || reflect.TypeOf(result) != reflect.TypeOf(target) || value.Kind() != reflect.Pointer || value.IsNil() {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func receiptMatches(receipt authority.MutationReceipt, intent authority.WriteIntent, operation, hash string) bool {
	key, err := (authority.OriginalOperationRequest{ProjectID: intent.ProjectID, IdempotencyKey: intent.IdempotencyKey}).KeyHash()
	parts := strings.Split(operation, ".")
	if err != nil || len(parts) != 2 || receipt.Validate() != nil || receipt.ProjectID != intent.ProjectID || receipt.IdempotencyKeyHash != key || receipt.IntentHash != hash || receipt.Citation.Kind != authority.EntityKind(parts[0]) {
		return false
	}
	if parts[1] == "create" {
		return receipt.Citation.Revision == 1
	}
	return receipt.Citation.EntityID == intent.EntityID && receipt.Citation.Revision == intent.ExpectedRevision+1
}

// Decode every success into its concrete schema BEFORE considering an optional
// caller destination. A nil result is not permission to skip peer validation.
func checkedAuthorityResult(method string, params any, raw []byte) (any, error) {
	bad := func() (any, error) { return nil, authority.Error{Code: authority.InvalidContract} }
	target := authorityResultTarget(method, params)
	if target == nil {
		return nil, authority.Error{Code: authority.Unsupported}
	}
	if err := authority.DecodeJSON(raw, target); err != nil {
		return nil, err
	}
	switch value := target.(type) {
	case *authority.Negotiation: // Validate already checked finite method/schema.
	case *authority.ProjectStatus:
		if value.ProjectID != params.(authority.ReadRequest).ProjectID {
			return bad()
		}
	case *authority.ReadResult:
		r := params.(authority.ReadRequest)
		if value.ProjectID != r.ProjectID {
			return bad()
		}
		limit := r.Limit
		if limit == 0 {
			limit = 50
		}
		if r.Operation == "get" {
			if len(value.Entities) != 1 || value.NextCursor != "" {
				return bad()
			}
		} else if len(value.Entities) > limit {
			return bad()
		}
		var last uint64
		seen := map[authority.UUID]bool{}
		for _, entity := range value.Entities {
			if entity.Citation.Kind != r.Kind {
				return bad()
			}
			if r.Operation == "get" || r.Operation == "history" {
				if entity.Citation.EntityID != r.EntityID || r.Revision != 0 && entity.Citation.Revision != r.Revision {
					return bad()
				}
				if r.Operation == "history" && entity.Citation.Revision <= last {
					return bad()
				}
				last = entity.Citation.Revision
			} else {
				alias, err := strconv.ParseUint(entity.DisplayAlias[strings.LastIndexByte(entity.DisplayAlias, '-')+1:], 10, 64)
				if err != nil || alias <= last || seen[entity.Citation.EntityID] {
					return bad()
				}
				last = alias
				seen[entity.Citation.EntityID] = true
			}
		}
	case *authority.MutationReceipt:
		if method == "AuthorityV1.MutationReceipt" {
			r := params.(authority.OriginalOperationRequest)
			key, _ := r.KeyHash()
			if value.ProjectID != r.ProjectID || value.IdempotencyKeyHash != key {
				return bad()
			}
		} else {
			r := params.(authority.AttributedMutationRequest)
			hash, err := localauthority.MutationIntentHash(r)
			if err != nil || !receiptMatches(*value, r.Request.Intent, r.Request.Operation, hash) {
				return bad()
			}
		}
	case *authority.DocumentMutationResult:
		r := params.(authority.DocumentMutationCall)
		hash, err := localauthority.DocumentIntentHash(r)
		intent := authority.WriteIntent{ProjectID: r.Request.Intent.ProjectID, WriteMode: r.Request.Intent.WriteMode, IdempotencyKey: r.Request.Intent.IdempotencyKey, EntityID: r.Request.Intent.EntityID, ExpectedRevision: r.Request.Intent.ExpectedRevision}
		if err != nil || !receiptMatches(value.Receipt, intent, r.Request.Operation, hash) {
			return bad()
		}
		if r.Request.Operation == "document.tombstone" {
			if value.Result.State != "tombstone" || value.Result.Tombstone.ParentOID != r.Request.Intent.ExpectedRefOID {
				return bad()
			}
		} else {
			var payload authority.DocumentPayload
			if authority.DecodeJSON(r.Request.Payload, &payload) != nil || value.Result.State != "live" {
				return bad()
			}
			digest := sha256.Sum256([]byte(payload.Markdown))
			if value.Result.Locator.SHA256 != hex.EncodeToString(digest[:]) || value.Result.Locator.ByteLength != int64(len(payload.Markdown)) {
				return bad()
			}
		}
	case *localauthority.KnowledgeState:
		if value.ProjectID != params.(authority.ProjectRequest).ProjectID {
			return bad()
		}
	case *localauthority.DocumentOperationResult:
		var r authority.OriginalOperationRequest
		if method == "AuthorityV1.CleanupDocumentPin" {
			r = params.(authority.CleanupDocumentRequest).Original()
		} else {
			r = params.(authority.OriginalOperationRequest)
		}
		if value.Match(r) != nil {
			return bad()
		}
		if method == "AuthorityV1.CleanupDocumentPin" && value.State.Phase != "settled" {
			return bad()
		}
	case *localauthority.RevisionMetadata:
		if value.Citation != params.(authority.Citation) {
			return bad()
		}
	case *localauthority.AuditResult:
		if value.Match(params.(authority.AuditRequest)) != nil {
			return bad()
		}
	default:
		return bad()
	}
	return target, nil
}

func authorityResultCopy(raw []byte, result any) error {
	if result == nil {
		return nil
	}
	return authority.DecodeJSON(raw, result)
}

func authorityFixedResponse(id any, code authority.Code) RPCResponse {
	if !authority.KnownCode(code) {
		code = authority.InvalidContract
	}
	return RPCResponse{JSONRPC: jsonrpcVersion, ID: id, Error: &RPCError{Code: -32000, Message: (authority.Error{Code: code}).Error(), DiagnosticCode: string(code)}}
}

func boundedAuthorityResponse(response RPCResponse) RPCResponse {
	raw, err := json.Marshal(response)
	if err != nil || len(raw)+1 > authority.MaxContractBytes {
		return authorityFixedResponse(response.ID, authority.Capacity)
	}
	return response
}
