package localauthority

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"unicode/utf8"

	"gitcode-mcp/internal/authority"
)

type SearchRequest struct {
	ProjectID authority.UUID       `json:"project_id"`
	Query     string               `json:"query"`
	Mode      string               `json:"mode,omitempty"`
	Kind      authority.EntityKind `json:"kind,omitempty"`
	Limit     int                  `json:"limit"`
	Cursor    string               `json:"cursor,omitempty"`
}

func (r SearchRequest) Validate() error {
	if !r.ProjectID.Valid() || len(r.Query) == 0 || len(r.Query) > 4096 || !utf8.ValidString(r.Query) || r.Kind != "" && !r.Kind.Valid() || r.Limit < 1 || r.Limit > 100 || len(r.Cursor) > 2048 || !utf8.ValidString(r.Cursor) || r.Mode != "" && r.Mode != "hybrid" && r.Mode != "full_text" {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

// SearchResult distinguishes requested semantic retrieval from real effective
// lexical work. Strict policy does not invent vectors or claim index coverage.
type SearchResult struct {
	LexicalResult  `json:"lexical"`
	RequestedMode  string `json:"requested_mode"`
	EffectiveMode  string `json:"effective_mode"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	SemanticState  string `json:"semantic_state"`
}

func (r SearchResult) Validate() error {
	if !r.ProjectID.Valid() || r.Sequence > math.MaxInt64 || r.Scanned < 0 || r.Scanned > lexicalEntityBudget || r.Partial != (r.NextCursor != "") || len(r.NextCursor) > 2048 || !utf8.ValidString(r.NextCursor) || r.Hits == nil || len(r.Hits) > 100 || r.EffectiveMode != "full_text" || r.SemanticState != "disabled_by_policy" {
		return authority.Error{Code: authority.InvalidContract}
	}
	if r.RequestedMode == "hybrid" {
		if r.FallbackReason != "policy_denied" {
			return authority.Error{Code: authority.InvalidContract}
		}
	} else if r.RequestedMode != "full_text" || r.FallbackReason != "" {
		return authority.Error{Code: authority.InvalidContract}
	}
	seen := map[authority.Citation]bool{}
	for _, h := range r.Hits {
		if h.Citation.Validate() != nil || h.Citation.ProjectID != r.ProjectID || seen[h.Citation] || !validDigest(h.ContentDigest) || h.Score < 1 || h.Score > 1000 || len(h.Snippet) > 512 || !utf8.ValidString(h.Snippet) {
			return authority.Error{Code: authority.InvalidContract}
		}
		seen[h.Citation] = true
	}
	return nil
}

type coordinatorSearchCursor struct {
	Mode    string `json:"mode"`
	Lexical string `json:"lexical"`
}

func (c *Coordinator) Search(ctx context.Context, r SearchRequest) (SearchResult, error) {
	var zero SearchResult
	if err := r.Validate(); err != nil {
		return zero, err
	}
	return coordinated(c, ctx, r.ProjectID, func(ctx context.Context, s *Store) (SearchResult, error) {
		mode := r.Mode
		if mode == "" {
			mode = s.manifest.Search.Default
		}
		if mode != "hybrid" && mode != "full_text" {
			return zero, Error{Corrupt}
		}
		cursor := coordinatorSearchCursor{Mode: mode}
		if r.Cursor != "" {
			body, err := base64.RawURLEncoding.DecodeString(r.Cursor)
			if err != nil || authority.DecodeJSON(body, &cursor) != nil || cursor.Mode != mode || cursor.Lexical == "" {
				return zero, authority.Error{Code: authority.InvalidContract}
			}
		}
		lexical, err := s.LexicalSearch(ctx, LexicalRequest{ProjectID: r.ProjectID, Query: r.Query, Kind: r.Kind, Limit: r.Limit, Cursor: cursor.Lexical})
		if err != nil {
			return zero, err
		}
		result := SearchResult{LexicalResult: lexical, RequestedMode: mode, EffectiveMode: "full_text", SemanticState: "disabled_by_policy"}
		if result.RequestedMode == "hybrid" {
			result.FallbackReason = "policy_denied"
		}
		if lexical.NextCursor != "" {
			cursor.Lexical = lexical.NextCursor
			body, _ := json.Marshal(cursor)
			result.NextCursor = base64.RawURLEncoding.EncodeToString(body)
		}
		if result.Validate() != nil {
			return zero, Error{Corrupt}
		}
		return result, nil
	})
}
