package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"gitcode-mcp/internal/cache"
)

// SourceMetadataFilters describes positive observations from cached metadata,
// never proof of a current/complete remote backlog or absence of a label.
type SourceMetadataFilters struct {
	Status        string   `json:"status,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	LabelsMatch   string   `json:"labels_match"`
	TitleContains string   `json:"title_contains,omitempty"`
	Scope         string   `json:"scope"`
	LabelEvidence string   `json:"label_evidence"`
}

func metadataFilters(status, title string, labels []string, match string) (*SourceMetadataFilters, error) {
	if len(labels) > 32 {
		return nil, ErrInvalidQuery{Field: "labels", Message: "at most 32 labels are allowed"}
	}
	seen := map[string]bool{}
	for _, label := range labels {
		if !utf8.ValidString(label) || strings.TrimSpace(label) == "" || len([]rune(label)) > 128 || strings.ContainsAny(label, "\x00\r\n") || seen[label] {
			return nil, ErrInvalidQuery{Field: "labels", Message: "labels must be distinct nonblank names of at most 128 characters, without NUL or newlines"}
		}
		seen[label] = true
	}
	if match != "" && match != "all" && match != "any" {
		return nil, ErrInvalidQuery{Field: "labels_match", Message: "labels_match must be all or any"}
	}
	if !utf8.ValidString(title) || len([]rune(title)) > 512 || strings.ContainsRune(title, 0) {
		return nil, ErrInvalidQuery{Field: "title_contains", Message: "title_contains must be at most 512 characters without NUL"}
	}
	if !utf8.ValidString(status) || len([]rune(status)) > 128 || strings.ContainsRune(status, 0) {
		return nil, ErrInvalidQuery{Field: "status", Message: "status must be at most 128 characters without NUL"}
	}
	if status == "" && title == "" && len(labels) == 0 {
		return nil, nil
	}
	if match == "" {
		match = "all"
	}
	return &SourceMetadataFilters{Status: status, Labels: append([]string(nil), labels...), LabelsMatch: match, TitleContains: title, Scope: "cached_sources", LabelEvidence: "positive_membership_only_unknown_excluded"}, nil
}

func sourceMatchesMetadata(source cache.Source, req SearchSourcesRequest) bool {
	if req.Kind != "" && source.Kind != req.Kind || req.Provenance != "" && string(source.Provenance) != req.Provenance || req.Status != "" && source.Status != req.Status || req.TitleContains != "" && !strings.Contains(source.Title, req.TitleContains) {
		return false
	}
	if len(req.Labels) == 0 {
		return true
	}
	count := 0
	for _, label := range req.Labels {
		for _, actual := range source.Labels {
			if label == actual {
				count++
				break
			}
		}
	}
	if req.LabelsMatch == "any" {
		return count > 0
	}
	return count == len(req.Labels)
}

func (s *Service) searchMetadataOnly(ctx context.Context, req SearchSourcesRequest, filters *SourceMetadataFilters, mode string, limit int) (SearchSourcesResult, error) {
	listed, err := s.listSources(ctx, ListSourcesRequest{RepoID: req.RepoID, Kind: req.Kind, Provenance: req.Provenance, Status: req.Status, Labels: req.Labels, LabelsMatch: req.LabelsMatch, TitleContains: req.TitleContains, Limit: limit, Offset: req.Offset})
	if err != nil {
		return SearchSourcesResult{}, err
	}
	results := make([]SearchSourceResult, 0, len(listed.Results))
	for i, source := range listed.Results {
		results = append(results, SearchSourceResult{RepoID: source.RepoID, ID: source.ID, Path: source.Path, Title: source.Title, Kind: source.Kind, Status: source.Status, Provenance: source.Provenance, Snippet: source.Title, Rank: req.Offset + i + 1, Citations: []SearchCitation{}})
	}
	return SearchSourcesResult{RepoID: listed.RepoID, Query: req.Query, Filters: filters, RequestedMode: mode, EffectiveMode: SearchModeFullText, SearchMode: SearchModeFullText, RAGState: "not_requested", Repair: SearchRepairStatus{State: "not_needed"}, Results: results, Limit: limit, Offset: req.Offset}, nil
}
