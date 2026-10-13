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
	case "AuthorityV1.EnqueueRecovery":
		req := params.(localauthority.RecoveryJobRequest)
		var result localauthority.RecoveryJobAdmission
		if authority.DecodeJSON(raw, &result) != nil || result.Validate() != nil || result.ProjectID != req.ProjectID || result.OperationID != req.OperationID {
			return nil, invalid
		}
		if result.Outcome == "queued" && result.Job.ObservedJournalGeneration < req.ObservedGeneration {
			return nil, invalid
		}
		return result, nil
	case "AuthorityV1.RecoveryJob":
		req := params.(localauthority.RecoveryJobLookup)
		var result localauthority.RecoveryJobView
		if authority.DecodeJSON(raw, &result) != nil || result.Validate() != nil || result.ProjectID != req.ProjectID || result.JobID != req.JobID {
			return nil, invalid
		}
		return result, nil
	case "AuthorityV1.CancelRecovery", "AuthorityV1.RetryRecovery":
		req := params.(localauthority.RecoveryJobAction)
		var result localauthority.RecoveryJobView
		if authority.DecodeJSON(raw, &result) != nil || result.Validate() != nil || result.ProjectID != req.ProjectID || result.JobID != req.JobID || result.Generation < req.ExpectedGeneration {
			return nil, invalid
		}
		// Correct UUIDs are not confirmation of an unperformed action.
		// Cancellation cannot leave queued/running permission acknowledged;
		// retry must advance a generation unless it is already succeeded.
		if method == "AuthorityV1.CancelRecovery" && (result.State == "queued" || result.State == "running") || method == "AuthorityV1.RetryRecovery" && result.Generation == req.ExpectedGeneration && result.State != "succeeded" {
			return nil, invalid
		}
		return result, nil
	case "AuthorityV1.RecoveryJobs":
		req := params.(localauthority.RecoveryJobsRequest)
		var result localauthority.RecoveryJobsPage
		if authority.DecodeJSON(raw, &result) != nil || localauthority.VerifyRecoveryJobsPage(req, result) != nil {
			return nil, invalid
		}
		return result, nil
	default:
		return nil, authority.Error{Code: authority.Unsupported}
	}
}
