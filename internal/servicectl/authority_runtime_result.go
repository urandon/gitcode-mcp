package servicectl

import (
	"encoding/json"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

// Validate concrete protected results even when callers discard the response.
// A parsed RPC success alone is not a confirmed identity/commit contract.
func authorityRuntimeResult(method string, params any, raw json.RawMessage) (any, error) {
	invalid := authority.Error{Code: authority.InvalidContract}
	if len(raw) > authority.MaxProtectedResultBytes {
		return nil, invalid
	}
	switch method {
	case "AuthorityV1.Read":
		req := params.(authority.ReadRequest)
		if req.Operation == "status" {
			var result authority.ProjectStatus
			if authority.DecodeJSON(raw, &result) != nil || result.Validate() != nil || result.ProjectID != req.ProjectID {
				return nil, invalid
			}
			return result, nil
		}
		var result authority.ReadResult
		if authority.DecodeJSON(raw, &result) != nil || result.Validate() != nil || result.ProjectID != req.ProjectID {
			return nil, invalid
		}
		for _, v := range result.Entities {
			if v.Citation.Kind != req.Kind || req.EntityID != "" && v.Citation.EntityID != req.EntityID || req.Operation == "get" && req.Revision != 0 && v.Citation.Revision != req.Revision {
				return nil, invalid
			}
		}
		if req.Limit > 0 && len(result.Entities) > req.Limit {
			return nil, invalid
		}
		if req.Operation == "get" && (len(result.Entities) != 1 || result.NextCursor != "") {
			return nil, invalid
		}
		return result, nil
	case "AuthorityV1.Mutate":
		req := params.(authority.MutationRequest)
		var result authority.MutationReceipt
		if authority.DecodeJSON(raw, &result) != nil || localauthority.VerifyMutationReceipt(req, localauthority.Actor{ID: "local-user"}, result) != nil {
			return nil, invalid
		}
		return result, nil
	case "AuthorityV1.MutateDocument":
		req := params.(authority.DocumentMutationRequest)
		var result authority.DocumentMutationResult
		if authority.DecodeJSON(raw, &result) != nil || localauthority.VerifyDocumentReceipt(req, localauthority.Actor{ID: "local-user"}, result) != nil {
			return nil, invalid
		}
		return result, nil
	case "AuthorityV1.KnowledgeStatus":
		var result localauthority.KnowledgeState
		if authority.DecodeJSON(raw, &result) != nil || result.Validate() != nil || result.ProjectID != params.(authorityProjectRequest).ProjectID {
			return nil, invalid
		}
		return result, nil
	case "AuthorityV1.Search":
		req := params.(localauthority.SearchRequest)
		var result localauthority.SearchResult
		// An omitted mode resolves from the immutable project manifest; an
		// explicit mode must be echoed exactly. Both results have finite schemas.
		if authority.DecodeJSON(raw, &result) != nil || result.Validate() != nil || result.ProjectID != req.ProjectID || req.Mode != "" && result.RequestedMode != req.Mode || len(result.Hits) > req.Limit {
			return nil, invalid
		}
		for _, h := range result.Hits {
			if req.Kind != "" && h.Citation.Kind != req.Kind {
				return nil, invalid
			}
		}
		return result, nil
	default:
		return nil, authority.Error{Code: authority.Unsupported}
	}
}
