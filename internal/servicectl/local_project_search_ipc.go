package servicectl

import (
	"encoding/json"
	"math"
	"strings"
	"unicode/utf8"

	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

func localJSONRequired(raw []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if strictObservationResponse(raw, &fields) != nil || fields == nil {
		return false
	}
	for _, key := range keys {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			return false
		}
	}
	return true
}

func localSearchRequestShape(raw []byte, required ...string) bool {
	if !utf8.Valid(raw) || !localJSONRequired(raw, required...) {
		return false
	}
	var fields map[string]json.RawMessage
	if strictObservationResponse(raw, &fields) != nil {
		return false
	}
	for _, value := range fields {
		if string(value) == "null" {
			return false
		}
	}
	return true
}

func localSearchIdentity(project, id, commit string) bool {
	return localStatusName.MatchString(project) && !strings.Contains(project, ".") && localStatusName.MatchString(id) && localStatusOID.MatchString(commit)
}

func validateLocalSearchResult(method string, raw []byte) (any, bool) {
	if method == "LocalProjects.Index" {
		var r service.LocalProjectIndexResult
		if strictObservationResponse(raw, &r) != nil || !localJSONRequired(raw, "project", "project_id", "commit", "revision_set_id", "state", "eligible_chunks", "embedded_chunks", "reused_chunks", "failed_chunks") || !localSearchIdentity(r.Project, r.ProjectID, r.Commit) || !strings.HasPrefix(r.RevisionSetID, "repo-doc-set-") || len(r.RevisionSetID) != len("repo-doc-set-")+64 || r.EligibleChunks < 0 || r.EligibleChunks > 32768 || r.EmbeddedChunks < 0 || r.EmbeddedChunks > 128 || r.ReusedChunks < 0 || r.FailedChunks < 0 || r.EmbeddedChunks+r.ReusedChunks+r.FailedChunks != r.EligibleChunks {
			return nil, false
		}
		if strings.Trim(strings.TrimPrefix(r.RevisionSetID, "repo-doc-set-"), "0123456789abcdef") != "" || r.State != "ready" && r.State != "partial" || r.State == "ready" && r.FailedChunks != 0 || r.State == "partial" && r.FailedChunks == 0 {
			return nil, false
		}
		return r, true
	}
	var r service.LocalProjectSearchResult
	if strictObservationResponse(raw, &r) != nil || !localJSONRequired(raw, "project", "project_id", "commit", "requested_mode", "effective_mode", "index_state", "coverage", "hits") || !localSearchIdentity(r.Project, r.ProjectID, r.Commit) || r.Hits == nil || len(r.Hits) > 32 {
		return nil, false
	}
	if r.RequestedMode != "fulltext" && r.RequestedMode != "hybrid" || r.EffectiveMode != "fulltext" && r.EffectiveMode != "hybrid" {
		return nil, false
	}
	switch r.IndexState {
	case "not_requested", "unavailable", "missing", "stale", "partial", "failed", "ready":
	default:
		return nil, false
	}
	switch r.Fallback {
	case "", "profile_disabled", "profile_unavailable", "index_unavailable", "index_missing", "index_stale", "index_partial", "index_failed", "semantic_unavailable":
	default:
		return nil, false
	}
	if r.RequestedMode == "fulltext" && (r.EffectiveMode != "fulltext" || r.IndexState != "not_requested" || r.Fallback != "") || r.EffectiveMode == "hybrid" && (r.IndexState != "ready" || r.Fallback != "") || r.RequestedMode == "hybrid" && r.EffectiveMode == "fulltext" && r.Fallback == "" {
		return nil, false
	}
	var wire struct {
		Coverage json.RawMessage   `json:"coverage"`
		Hits     []json.RawMessage `json:"hits"`
	}
	if json.Unmarshal(raw, &wire) != nil || !localJSONRequired(wire.Coverage, "state", "eligible_files", "eligible_chunks", "embedded_chunks", "reused_chunks", "failed_chunks", "missing_objects") {
		return nil, false
	}
	c := r.Coverage
	if c.State != "lexical-only" && c.State != "ready" || c.EligibleFiles < 0 || c.EligibleFiles > 10000 || c.EligibleChunks < 0 || c.EligibleChunks > 32768 || c.EmbeddedChunks < 0 || c.ReusedChunks < 0 || c.FailedChunks < 0 || c.MissingObjects < 0 || c.EmbeddedChunks+c.ReusedChunks+c.FailedChunks > 32768 || c.MissingObjects > 10000 {
		return nil, false
	}
	if r.EffectiveMode == "hybrid" && (c.State != "ready" || c.EmbeddedChunks+c.ReusedChunks != c.EligibleChunks || c.FailedChunks != 0 || c.MissingObjects != 0) || r.EffectiveMode == "fulltext" && c.State != "lexical-only" || r.RequestedMode == "hybrid" && r.IndexState == "not_requested" {
		return nil, false
	}
	for i, hit := range r.Hits {
		if !localJSONRequired(wire.Hits[i], "citation", "snippet", "line_start", "line_end", "digest", "score") || localgit.ValidateCitationResponse(hit.Citation) != nil || hit.Citation.ProjectID != r.ProjectID || hit.Citation.Commit != r.Commit || len(hit.Snippet) > 800 || !utf8.ValidString(hit.Snippet) || hit.LineStart < 1 || hit.LineEnd < hit.LineStart || hit.LineEnd > localgit.MaxFileBytes+1 || len(hit.Digest) != 64 || strings.Trim(hit.Digest, "0123456789abcdef") != "" || math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) || hit.Score < 0 || hit.Score > 2 {
			return nil, false
		}
	}
	return r, true
}
