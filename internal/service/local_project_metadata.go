package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"unicode/utf8"

	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/repositorydocs"
)

func localQueryHasMetadata(q LocalProjectQuery) bool {
	return q.Status != "" || len(q.Labels) != 0 || q.LabelsMatch != "" || q.TitleContains != ""
}

func localIssuePredicate(status, title string, labels []string, match string) (func(localgit.Metadata) bool, error) {
	filters, err := metadataFilters(status, title, labels, match)
	if err != nil {
		var invalid ErrInvalidQuery
		if errors.As(err, &invalid) {
			return nil, localProjectError("invalid_argument", invalid.Field)
		}
		return nil, localProjectError("invalid_argument", "filters")
	}
	if filters == nil {
		return nil, nil
	}
	return func(m localgit.Metadata) bool {
		return m.Kind == localgit.Issue && matchesMetadata(m.State, m.Title, m.Labels, filters.Status, filters.TitleContains, filters.Labels, filters.LabelsMatch)
	}, nil
}

// Metadata-only matches use exact Git prefix slices, not synthetic title text
// masquerading as a line/digest citation. No index or provider is opened here.
func localMetadataSearch(ctx context.Context, q LocalProjectSearchRequest, projectID, commit string, repo *repositorydocs.Repository, citations map[string]localgit.Citation) (LocalProjectSearchResult, error) {
	out := LocalProjectSearchResult{Project: q.Project, ProjectID: projectID, Commit: commit, RequestedMode: q.Mode, EffectiveMode: "fulltext", IndexState: "not_requested", MetadataOnly: true, Coverage: repositorydocs.SearchCoverage{State: "lexical-only", EligibleFiles: len(citations)}, Hits: []LocalProjectSearchHit{}}
	if q.Mode == "hybrid" {
		out.Fallback = "metadata_only"
	}
	paths := make([]string, 0, len(citations))
	for path := range citations {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > q.Limit {
		paths = paths[:q.Limit]
	}
	for _, path := range paths {
		data, found, err := repo.ReadFileAtCommit(ctx, commit, path, localgit.MaxFileBytes)
		if err != nil || !found {
			return LocalProjectSearchResult{}, localProjectSearchError(ctx)
		}
		if len(data) > 800 {
			data = data[:800]
			for !utf8.Valid(data) {
				data = data[:len(data)-1]
			}
		}
		data = bytes.TrimRight(data, "\r\n")
		digest := sha256.Sum256(data)
		out.Hits = append(out.Hits, LocalProjectSearchHit{Citation: citations[path], Snippet: string(data), LineStart: 1, LineEnd: 1 + bytes.Count(data, []byte{'\n'}), Digest: hex.EncodeToString(digest[:]), Score: 1})
	}
	return out, nil
}
