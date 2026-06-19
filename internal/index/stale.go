package index

import (
	"context"
	"sort"
)

func StaleCheck(ctx context.Context, reader SourceReader, links DerivedLinkReader) (StaleReport, error) {
	sources, err := reader.ListSources(ctx)
	if err != nil {
		return StaleReport{}, err
	}
	derivedLinks, err := links.ListDerivedLinks(ctx)
	if err != nil {
		return StaleReport{}, err
	}
	brokenLinks, err := links.ListBrokenLinks(ctx)
	if err != nil {
		return StaleReport{}, err
	}
	anchors, err := links.ListCitationAnchors(ctx)
	if err != nil {
		return StaleReport{}, err
	}
	sourceIDs := map[string]bool{}
	for _, source := range sources {
		sourceIDs[source.ID] = true
	}
	anchorIDs := map[string]bool{}
	for _, anchor := range anchors {
		anchorIDs[anchor.ID] = true
	}
	affected := map[string]bool{}
	unresolved := map[string]bool{}
	brokenRaw := map[string]bool{}
	ambiguous := map[string]bool{}
	staleAnchors := map[string]bool{}
	staleCount := 0
	for _, link := range derivedLinks {
		if err := ctx.Err(); err != nil {
			return StaleReport{}, err
		}
		if !sourceIDs[link.TargetID] {
			staleCount++
			affected[link.SourceID] = true
			unresolved[link.RawTarget] = true
		}
		if link.TargetAnchor != "" && !anchorIDs[link.TargetAnchor] {
			staleCount++
			affected[link.SourceID] = true
			staleAnchors[link.TargetAnchor] = true
		}
	}
	for _, broken := range brokenLinks {
		staleCount++
		affected[broken.SourceID] = true
		brokenRaw[broken.Text] = true
		unresolved[broken.RawTarget] = true
		if broken.Reason == "ambiguous_alias" {
			ambiguous[broken.RawTarget] = true
		}
	}
	return StaleReport{TotalStaleBacklinks: staleCount, AffectedSourceIDs: sortedKeys(affected), UnresolvedTargets: sortedKeys(unresolved), BrokenRawLinkText: sortedKeys(brokenRaw), AmbiguousAliases: sortedKeys(ambiguous), StaleAnchorRefs: sortedKeys(staleAnchors)}, nil
}

func sortedKeys(values map[string]bool) []string {
	var keys []string
	for key := range values {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
