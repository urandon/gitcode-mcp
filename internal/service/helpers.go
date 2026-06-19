package service

import (
	"sort"
	"strings"

	"gitcode-mcp/internal/cache"
)

func nullableLine(line int) *int {
	if line <= 0 {
		return nil
	}
	v := line
	return &v
}

func sourceSummary(source cache.Source) SourceSummary {
	return SourceSummary{RepoID: source.RepoID, ID: source.ID, Path: source.Path, RemoteAlias: remoteAlias(source.Aliases), Kind: source.Kind, Title: source.Title, Status: source.Status, UpdatedAt: source.UpdatedAt.UTC()}
}

func sourceRecord(source cache.Source, links []cache.Link, backlinks []BacklinkResult) SourceRecord {
	labels := append([]string(nil), source.Labels...)
	sort.Strings(labels)
	return SourceRecord{RepoID: source.RepoID, ID: source.ID, Path: source.Path, RemoteAlias: remoteAlias(source.Aliases), Kind: source.Kind, Title: source.Title, Body: source.Body, Status: source.Status, Labels: labels, Links: linkResults(links), Backlinks: backlinks, UpdatedAt: source.UpdatedAt.UTC()}
}

func linkResults(links []cache.Link) []LinkResult {
	out := make([]LinkResult, 0, len(links))
	for _, link := range links {
		out = append(out, LinkResult{SourceID: link.SourceID, TargetID: link.TargetID, Kind: link.Kind, Text: link.Text})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetID != out[j].TargetID {
			return out[i].TargetID < out[j].TargetID
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Text < out[j].Text
	})
	return out
}

func remoteAlias(aliases []cache.Identity) string {
	candidates := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if alias.Remote.Type != "" && alias.Remote.ID != "" {
			candidates = append(candidates, alias.Remote.Type+":"+alias.Remote.ID)
			continue
		}
		if alias.AliasType != "" && alias.Alias != "" {
			candidates = append(candidates, alias.AliasType+":"+alias.Alias)
		}
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

func sliceSources(sources []cache.Source, offset, limit int) []cache.Source {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(sources) {
		return nil
	}
	sources = sources[offset:]
	if limit > 0 && limit < len(sources) {
		return sources[:limit]
	}
	return sources
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func normalizeFormat(format string) string {
	if strings.EqualFold(format, "json") {
		return "json"
	}
	if strings.EqualFold(format, "markdown") || strings.EqualFold(format, "md") {
		return "markdown"
	}
	return "text"
}

func changedIDs(base, head string) []string {
	ids := map[string]struct{}{}
	for _, line := range strings.Split(head, "\n") {
		if line == "" || strings.Contains(base, line) {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) > 0 && parts[0] != "" {
			ids[parts[0]] = struct{}{}
		}
	}
	return sortedKeys(ids)
}

func simpleDiff(base, head string) string {
	if base == head {
		return ""
	}
	return "--- base\n+++ head\n" + head
}
