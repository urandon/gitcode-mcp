package index

import (
	"context"
	"sort"
	"time"
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
	return staleReport(ctx, sources, derivedLinks, brokenLinks, anchors)
}

type IndexStateReader interface {
	ListIndexStates(context.Context, ChunkQuery) ([]IndexState, error)
	ListChunksForFreshness(context.Context, ChunkQuery) ([]Chunk, error)
	ListCitationAnchors(context.Context, ChunkQuery) ([]CitationAnchor, error)
}

func FreshnessCheck(ctx context.Context, reader SourceReader, states IndexStateReader, links DerivedLinkReader, query ChunkQuery) (IndexFreshnessReport, error) {
	sources, err := reader.ListSources(ctx)
	if err != nil {
		return IndexFreshnessReport{}, err
	}
	stateRows, err := states.ListIndexStates(ctx, query)
	if err != nil {
		return IndexFreshnessReport{}, err
	}
	chunks, err := states.ListChunksForFreshness(ctx, query)
	if err != nil {
		return IndexFreshnessReport{}, err
	}
	anchors, err := states.ListCitationAnchors(ctx, query)
	if err != nil {
		return IndexFreshnessReport{}, err
	}
	derivedLinks, err := links.ListDerivedLinks(ctx)
	if err != nil {
		return IndexFreshnessReport{}, err
	}
	brokenLinks, err := links.ListBrokenLinks(ctx)
	if err != nil {
		return IndexFreshnessReport{}, err
	}
	linkAnchors, err := links.ListCitationAnchors(ctx)
	if err != nil {
		return IndexFreshnessReport{}, err
	}
	linkReport, err := staleReport(ctx, sources, derivedLinks, brokenLinks, linkAnchors)
	if err != nil {
		return IndexFreshnessReport{}, err
	}
	return BuildFreshnessReport(ctx, sources, stateRows, chunks, anchors, linkReport, query), nil
}

func BuildFreshnessReport(ctx context.Context, sources []SourceRecord, states []IndexState, chunks []Chunk, anchors []CitationAnchor, linkReport StaleReport, query ChunkQuery) IndexFreshnessReport {
	stateBySource := map[string]IndexState{}
	for _, state := range states {
		if !stateMatchesQuery(state, query) {
			continue
		}
		key := freshnessKey(state.RepoID, state.SourceID, state.RecordID, state.SnapshotID, state.Policy)
		stateBySource[key] = state
	}
	chunkStats := map[string]IndexState{}
	for _, chunk := range chunks {
		if !chunkMatchesQuery(chunk, query) {
			continue
		}
		key := freshnessKey(chunk.RepoID, chunk.SourceID, chunk.RecordID, chunk.SnapshotID, chunk.Policy)
		stat := chunkStats[key]
		stat.RepoID = chunk.RepoID
		stat.SourceID = chunk.SourceID
		stat.RecordID = chunk.RecordID
		stat.SnapshotID = chunk.SnapshotID
		stat.Policy = chunk.Policy
		stat.ChunkCount++
		if stat.ContentHash == "" {
			stat.ContentHash = chunk.ContentHash
		}
		stat = indexStateFromChunkMetadata(stat, chunk)
		chunkStats[key] = stat
	}
	citationCounts := map[string]int{}
	for _, anchor := range anchors {
		if !citationAnchorMatchesQuery(anchor, query) {
			continue
		}
		citationCounts[freshnessKey(anchor.RepoID, anchor.SourceID, anchor.RecordID, anchor.SnapshotID, anchor.Policy)]++
	}
	missingTargets := setFromStrings(linkReport.UnresolvedTargets)
	staleAnchors := setFromStrings(linkReport.StaleAnchorRefs)
	affected := setFromStrings(linkReport.AffectedSourceIDs)
	var records []IndexFreshnessRecord
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return IndexFreshnessReport{Records: records, Warnings: warningsFromFreshnessRecords(records)}
		}
		if query.RepoID != "" && source.RepoID != query.RepoID {
			continue
		}
		if query.SourceID != "" && source.ID != query.SourceID {
			continue
		}
		recordID := sourceRecordID(source)
		if query.RecordID != "" && recordID != query.RecordID {
			continue
		}
		policy := query.Policy
		if policy == "" {
			policy = ChunkPolicyHeading
		}
		key := freshnessKey(source.RepoID, source.ID, recordID, source.SnapshotID, policy)
		state, ok := stateBySource[key]
		stat := chunkStats[key]
		if !ok && stat.ChunkCount > 0 {
			state = stat
			ok = true
		}
		if state.ChunkCount == 0 {
			state.ChunkCount = stat.ChunkCount
		}
		if state.CitationCount == 0 {
			state.CitationCount = citationCounts[key]
		}
		currentHash := ContentHash(source.Body)
		if source.Body == "" {
			currentHash = source.Metadata["content_hash"]
		}
		record := IndexFreshnessRecord{RepoID: source.RepoID, SourceID: source.ID, RecordID: recordID, SnapshotID: source.SnapshotID, Policy: policy, CurrentContentHash: currentHash, IndexedContentHash: state.ContentHash, CurrentRemoteRevision: source.RemoteRevision, IndexedRemoteRevision: state.RemoteRevision, CurrentSyncRevision: source.SyncRevision, IndexedSyncRevision: state.SyncRevision, CurrentSyncEventID: source.SyncEventID, IndexedSyncEventID: state.SyncEventID, CurrentUpdatedAt: source.UpdatedAt.UTC(), IndexedUpdatedAt: state.SourceUpdatedAt.UTC(), IndexedAt: state.IndexedAt.UTC(), ChunkCount: state.ChunkCount, CitationCount: state.CitationCount}
		if affected[source.ID] {
			record.MissingTargetIDs = sortedKeys(missingTargets)
			record.StaleAnchorRefs = sortedKeys(staleAnchors)
		}
		switch {
		case !ok || state.ChunkCount == 0:
			record.State = IndexFreshnessMissingIndex
			record.WarningCode = WarningMissingIndex
		case currentHash != "" && state.ContentHash != "" && currentHash != state.ContentHash:
			record.State = IndexFreshnessStaleByContent
			record.WarningCode = WarningStaleIndex
		case revisionChanged(source, state):
			record.State = IndexFreshnessStaleByRevision
			record.WarningCode = WarningStaleIndexRevision
		case affected[source.ID]:
			record.State = IndexFreshnessLinkStaleOnly
			record.WarningCode = WarningLinkStaleOnly
			record.LinkStaleCount = linkReport.TotalStaleBacklinks
			record.MissingTargetIDs = sortedKeys(missingTargets)
			record.StaleAnchorRefs = sortedKeys(staleAnchors)
		default:
			record.State = IndexFreshnessFresh
		}
		records = append(records, record)
	}
	sortFreshnessRecords(records)
	return IndexFreshnessReport{Records: records, Warnings: warningsFromFreshnessRecords(records)}
}

func staleReport(ctx context.Context, sources []SourceRecord, derivedLinks []DerivedLink, brokenLinks []BrokenLink, anchors []CitationAnchor) (StaleReport, error) {
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

func stateMatchesQuery(state IndexState, query ChunkQuery) bool {
	if query.RepoID != "" && state.RepoID != query.RepoID {
		return false
	}
	if query.SourceID != "" && state.SourceID != query.SourceID {
		return false
	}
	if query.RecordID != "" && state.RecordID != query.RecordID {
		return false
	}
	if query.SnapshotID != "" && state.SnapshotID != query.SnapshotID {
		return false
	}
	return query.Policy == "" || state.Policy == query.Policy
}

func chunkMatchesQuery(chunk Chunk, query ChunkQuery) bool {
	if query.RepoID != "" && chunk.RepoID != query.RepoID {
		return false
	}
	if query.SourceID != "" && chunk.SourceID != query.SourceID {
		return false
	}
	if query.RecordID != "" && chunk.RecordID != query.RecordID {
		return false
	}
	if query.SnapshotID != "" && chunk.SnapshotID != query.SnapshotID {
		return false
	}
	return query.Policy == "" || chunk.Policy == query.Policy
}

func citationAnchorMatchesQuery(anchor CitationAnchor, query ChunkQuery) bool {
	if query.RepoID != "" && anchor.RepoID != "" && anchor.RepoID != query.RepoID {
		return false
	}
	if query.SourceID != "" && anchor.SourceID != query.SourceID {
		return false
	}
	if query.RecordID != "" && anchor.RecordID != "" && anchor.RecordID != query.RecordID {
		return false
	}
	if query.SnapshotID != "" && anchor.SnapshotID != "" && anchor.SnapshotID != query.SnapshotID {
		return false
	}
	if query.Policy != "" && anchor.Policy != "" && anchor.Policy != query.Policy {
		return false
	}
	return true
}

func indexStateFromChunkMetadata(state IndexState, chunk Chunk) IndexState {
	if state.RecordID == "" {
		state.RecordID = chunk.RecordID
	}
	if state.SnapshotID == "" {
		state.SnapshotID = chunk.SnapshotID
	}
	if state.RemoteRevision == "" {
		state.RemoteRevision = chunk.InheritedMetadata["remote_revision"]
	}
	if state.SyncRevision == "" {
		state.SyncRevision = chunk.InheritedMetadata["sync_revision"]
	}
	if state.SyncEventID == "" {
		state.SyncEventID = chunk.InheritedMetadata["sync_event_id"]
	}
	if state.SourceUpdatedAt.IsZero() {
		state.SourceUpdatedAt = parseTime(chunk.InheritedMetadata["source_updated_at"])
	}
	if state.IndexedAt.IsZero() {
		state.IndexedAt = parseTime(chunk.InheritedMetadata["indexed_at"])
	}
	return state
}

func revisionChanged(source SourceRecord, state IndexState) bool {
	if source.RemoteRevision != "" && state.RemoteRevision != "" && source.RemoteRevision != state.RemoteRevision {
		return true
	}
	if source.SyncRevision != "" && state.SyncRevision != "" && source.SyncRevision != state.SyncRevision {
		return true
	}
	if source.SyncEventID != "" && state.SyncEventID != "" && source.SyncEventID != state.SyncEventID {
		return true
	}
	return !source.UpdatedAt.IsZero() && !state.SourceUpdatedAt.IsZero() && source.UpdatedAt.After(state.SourceUpdatedAt)
}

func warningsFromFreshnessRecords(records []IndexFreshnessRecord) []IndexWarning {
	warnings := []IndexWarning{}
	for _, record := range records {
		if record.WarningCode == "" {
			continue
		}
		warnings = append(warnings, IndexWarning{RepoID: record.RepoID, SourceID: record.SourceID, RecordID: record.RecordID, SnapshotID: record.SnapshotID, Policy: record.Policy, State: record.State, Code: record.WarningCode, Message: freshnessMessage(record)})
	}
	return warnings
}

func freshnessMessage(record IndexFreshnessRecord) string {
	switch record.WarningCode {
	case WarningMissingIndex:
		return "source has no indexed chunks for requested policy"
	case WarningStaleIndex:
		return "source content changed after indexing"
	case WarningStaleIndexRevision:
		return "source revision changed after indexing"
	case WarningLinkStaleOnly:
		return "source chunks are fresh but derived links or citation anchors are stale"
	default:
		return "index freshness warning"
	}
}

func sortFreshnessRecords(records []IndexFreshnessRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if a.RepoID != b.RepoID {
			return a.RepoID < b.RepoID
		}
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		if a.RecordID != b.RecordID {
			return a.RecordID < b.RecordID
		}
		if statePrecedence(a.State) != statePrecedence(b.State) {
			return statePrecedence(a.State) < statePrecedence(b.State)
		}
		return a.WarningCode < b.WarningCode
	})
}

func statePrecedence(state IndexFreshnessState) int {
	switch state {
	case IndexFreshnessMissingIndex:
		return 0
	case IndexFreshnessStaleByContent:
		return 1
	case IndexFreshnessStaleByRevision:
		return 2
	case IndexFreshnessLinkStaleOnly:
		return 3
	case IndexFreshnessFresh:
		return 4
	default:
		return 5
	}
}

func freshnessKey(repoID, sourceID, recordID, snapshotID string, policy ChunkPolicy) string {
	if policy == "" {
		policy = ChunkPolicyHeading
	}
	return repoID + "\x00" + sourceID + "\x00" + recordID + "\x00" + snapshotID + "\x00" + string(policy)
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}

func setFromStrings(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
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
