package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
)

func snapshotHash(value any) (string, error) {
	b, err := marshalCanonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func snapshotManifest(snapshot Snapshot) Snapshot {
	snapshot.Chunks = nil
	return snapshot
}

func renderSnapshotContent(snapshot Snapshot, format string) ([]byte, error) {
	sortSnapshot(&snapshot)
	switch normalizeSnapshotFormat(format) {
	case "json":
		return marshalCanonicalJSON(snapshot)
	case "markdown", "text":
		return renderSnapshotMarkdown(snapshot), nil
	default:
		return nil, ErrInvalidQuery{Field: "format", Message: "format must be json or markdown"}
	}
}

func marshalCanonicalJSON(value any) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	return b, nil
}

func renderSnapshotMarkdown(snapshot Snapshot) []byte {
	var b strings.Builder
	b.WriteString("# Corpus Snapshot\n\n")
	b.WriteString("schema_version: " + snapshot.SchemaVersion + "\n")
	b.WriteString("created_at: " + formatSnapshotTime(snapshot.CreatedAt) + "\n\n")
	b.WriteString("## Sources\n\n")
	for _, source := range snapshot.Sources {
		b.WriteString("### " + source.ID + "\n\n")
		b.WriteString("- path: " + source.Path + "\n")
		b.WriteString("- kind: " + source.Kind + "\n")
		b.WriteString("- status: " + source.Status + "\n")
		b.WriteString("- title: " + source.Title + "\n")
		b.WriteString("- content_hash: " + source.ContentHash + "\n")
		if len(source.Labels) > 0 {
			b.WriteString("- labels: " + strings.Join(source.Labels, ",") + "\n")
		}
		if source.Body != "" {
			b.WriteString("\n```\n" + source.Body + "\n```\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Aliases\n\n")
	for _, alias := range snapshot.Aliases {
		b.WriteString(fmt.Sprintf("- %s %s:%s %s:%s\n", alias.SourceID, alias.AliasKind, alias.AliasValue, alias.RemoteKind, alias.RemoteID))
	}
	b.WriteString("\n## Links\n\n")
	for _, link := range snapshot.Links {
		b.WriteString(fmt.Sprintf("- %s -> %s %s %s\n", link.SourceID, link.TargetID, link.LinkType, link.Text))
	}
	b.WriteString("\n## Sync Status\n\n")
	for _, status := range snapshot.SyncStatus {
		b.WriteString(fmt.Sprintf("- %s %s %s %s %s\n", status.SourceID, status.Status, status.Freshness, status.RemoteType, status.RemoteID))
	}
	b.WriteString("\n## Chunks\n\n")
	for _, chunk := range snapshot.Chunks {
		b.WriteString(fmt.Sprintf("- %s %s %d-%d lines=%d-%d hash=%s heading=%s\n", chunk.ID, chunk.SourceID, chunk.ByteStart, chunk.ByteEnd, chunk.LineStart, chunk.LineEnd, chunk.ContentHash, strings.Join(chunk.HeadingPath, " > ")))
	}
	if len(snapshot.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, warning := range snapshot.Warnings {
			b.WriteString(fmt.Sprintf("- %s %s %s %s\n", warning.Code, warning.State, warning.SourceID, warning.Message))
		}
	}
	return []byte(b.String())
}

func normalizeSnapshotFormat(format string) string {
	if strings.EqualFold(format, "") || strings.EqualFold(format, "json") {
		return "json"
	}
	return normalizeFormat(format)
}

func parseSnapshotBytes(b []byte, format string) (Snapshot, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return Snapshot{SchemaVersion: "gitcode-mcp.snapshot.v1", CreatedAt: time.Unix(0, 0).UTC()}, nil
	}
	var snapshot Snapshot
	if err := json.Unmarshal(b, &snapshot); err != nil {
		return parseLegacyTextSnapshot(string(b)), nil
	}
	sortSnapshot(&snapshot)
	return snapshot, nil
}

func parseLegacyTextSnapshot(content string) Snapshot {
	snapshot := Snapshot{SchemaVersion: "gitcode-mcp.snapshot.v1", CreatedAt: time.Unix(0, 0).UTC()}
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			continue
		}
		updated, _ := time.Parse(time.RFC3339Nano, parts[6])
		if updated.IsZero() {
			updated, _ = time.Parse(time.RFC3339, parts[6])
		}
		labels := []string{}
		if parts[5] != "" {
			labels = strings.Split(parts[5], ",")
			sort.Strings(labels)
		}
		snapshot.Sources = append(snapshot.Sources, SnapshotSource{ID: parts[0], Path: parts[1], Kind: parts[2], Status: parts[3], Title: parts[4], Labels: labels, UpdatedAt: updated.UTC()})
	}
	sortSnapshot(&snapshot)
	return snapshot
}

func sortSnapshot(snapshot *Snapshot) {
	sort.SliceStable(snapshot.Sources, func(i, j int) bool { return snapshot.Sources[i].ID < snapshot.Sources[j].ID })
	sort.SliceStable(snapshot.Aliases, func(i, j int) bool { return aliasKey(snapshot.Aliases[i]) < aliasKey(snapshot.Aliases[j]) })
	sort.SliceStable(snapshot.Links, func(i, j int) bool { return linkKey(snapshot.Links[i]) < linkKey(snapshot.Links[j]) })
	sort.SliceStable(snapshot.Backlinks, func(i, j int) bool { return linkKey(snapshot.Backlinks[i]) < linkKey(snapshot.Backlinks[j]) })
	sort.SliceStable(snapshot.SyncStatus, func(i, j int) bool { return snapshot.SyncStatus[i].SourceID < snapshot.SyncStatus[j].SourceID })
	sort.SliceStable(snapshot.Warnings, func(i, j int) bool {
		a, b := snapshot.Warnings[i], snapshot.Warnings[j]
		if a.RepoID != b.RepoID {
			return a.RepoID < b.RepoID
		}
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		if a.RecordID != b.RecordID {
			return a.RecordID < b.RecordID
		}
		return a.Code < b.Code
	})
	sort.SliceStable(snapshot.Chunks, func(i, j int) bool {
		a, c := snapshot.Chunks[i], snapshot.Chunks[j]
		if a.SourceID != c.SourceID {
			return a.SourceID < c.SourceID
		}
		if a.ContentHash != c.ContentHash {
			return a.ContentHash < c.ContentHash
		}
		if a.ByteStart != c.ByteStart {
			return a.ByteStart < c.ByteStart
		}
		return a.ID < c.ID
	})
	for i := range snapshot.Sources {
		sort.Strings(snapshot.Sources[i].Labels)
	}
	for i := range snapshot.Chunks {
		snapshot.Chunks[i].OutboundLinks = sortedStrings(snapshot.Chunks[i].OutboundLinks)
		snapshot.Chunks[i].InheritedMetadata = copyStringMap(snapshot.Chunks[i].InheritedMetadata)
		snapshot.Chunks[i].ResolvedAliases = copyStringMap(snapshot.Chunks[i].ResolvedAliases)
	}
}

func cacheSnapshotChunks(snapshot Snapshot, snapshotID string) []cache.SnapshotChunk {
	chunks := make([]cache.SnapshotChunk, 0, len(snapshot.Chunks))
	for i, chunk := range snapshot.Chunks {
		heading, _ := json.Marshal(chunk.HeadingPath)
		revision := chunk.SourceRevisionHash
		if revision == "" {
			for _, status := range snapshot.SyncStatus {
				if status.SourceID == chunk.SourceID {
					revision = status.RemoteRevision
					break
				}
			}
		}
		metadata, _ := json.Marshal(chunk.InheritedMetadata)
		links, _ := json.Marshal(chunk.OutboundLinks)
		aliases, _ := json.Marshal(chunk.ResolvedAliases)
		chunks = append(chunks, cache.SnapshotChunk{RepoID: snapshot.RepoID, SnapshotID: snapshotID, ChunkID: chunk.ID, SourceType: sourceKind(snapshot.Sources, chunk.SourceID), SourceID: chunk.SourceID, RecordID: firstNonEmptyString(chunk.RecordID, chunk.SourceID), SourceContentHash: chunk.ContentHash, SourceRevisionHash: revision, IndexBuildID: firstNonEmptyString(chunk.IndexBuildID, snapshotID), ChunkContentHash: chunk.ContentHash, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPathJSON: string(heading), Ordinal: i, Text: chunk.Text, MetadataJSON: string(metadata), OutboundLinksJSON: string(links), ResolvedAliasesJSON: string(aliases), Citation: fmt.Sprintf("%s:%d-%d", chunk.SourceID, chunk.LineStart, chunk.LineEnd), ContentHash: chunk.ContentHash})
	}
	return chunks
}

func snapshotChunksFromCache(chunks []cache.SnapshotChunk) []SnapshotChunk {
	out := make([]SnapshotChunk, 0, len(chunks))
	for _, chunk := range chunks {
		var heading []string
		_ = json.Unmarshal([]byte(chunk.HeadingPathJSON), &heading)
		metadata := map[string]string{}
		links := []string{}
		aliases := map[string]string{}
		_ = json.Unmarshal([]byte(chunk.MetadataJSON), &metadata)
		_ = json.Unmarshal([]byte(chunk.OutboundLinksJSON), &links)
		_ = json.Unmarshal([]byte(chunk.ResolvedAliasesJSON), &aliases)
		out = append(out, SnapshotChunk{RepoID: chunk.RepoID, ID: chunk.ChunkID, SourceID: chunk.SourceID, RecordID: chunk.RecordID, ContentHash: firstNonEmptyString(chunk.ChunkContentHash, chunk.ContentHash), ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPath: heading, Text: chunk.Text, InheritedMetadata: metadata, OutboundLinks: links, ResolvedAliases: aliases, SourceRevisionHash: chunk.SourceRevisionHash, IndexBuildID: chunk.IndexBuildID, Ordinal: chunk.Ordinal})
	}
	return out
}

func snapshotChunkHashRows(chunks []cache.SnapshotChunk) []map[string]any {
	chunks = append([]cache.SnapshotChunk(nil), chunks...)
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].SourceType != chunks[j].SourceType {
			return chunks[i].SourceType < chunks[j].SourceType
		}
		if chunks[i].SourceID != chunks[j].SourceID {
			return chunks[i].SourceID < chunks[j].SourceID
		}
		if chunks[i].RecordID != chunks[j].RecordID {
			return chunks[i].RecordID < chunks[j].RecordID
		}
		if chunks[i].Ordinal != chunks[j].Ordinal {
			return chunks[i].Ordinal < chunks[j].Ordinal
		}
		return chunks[i].ChunkID < chunks[j].ChunkID
	})
	rows := make([]map[string]any, 0, len(chunks))
	for _, chunk := range chunks {
		rows = append(rows, map[string]any{"chunk_id": chunk.ChunkID, "source_type": chunk.SourceType, "source_id": chunk.SourceID, "record_id": chunk.RecordID, "source_content_hash": chunk.SourceContentHash, "source_revision_hash": chunk.SourceRevisionHash, "index_build_id": chunk.IndexBuildID, "chunk_content_hash": chunk.ChunkContentHash, "byte_start": chunk.ByteStart, "byte_end": chunk.ByteEnd, "line_start": chunk.LineStart, "line_end": chunk.LineEnd, "heading_path": chunk.HeadingPathJSON, "ordinal": chunk.Ordinal, "text_hash": textHash(chunk.Text), "metadata_json": chunk.MetadataJSON, "outbound_links_json": chunk.OutboundLinksJSON, "resolved_aliases_json": chunk.ResolvedAliasesJSON})
	}
	return rows
}

func sourceKind(sources []SnapshotSource, sourceID string) string {
	for _, source := range sources {
		if source.ID == sourceID {
			return source.Kind
		}
	}
	return ""
}

func textHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func diffSnapshots(base, head Snapshot) DiffSnapshotResult {
	result := DiffSnapshotResult{ChangedSourceIDs: []string{}, AddedSourceIDs: []string{}, RemovedSourceIDs: []string{}, ModifiedSourceIDs: []string{}}
	baseSources := mapSources(base.Sources)
	headSources := mapSources(head.Sources)
	changedSourceIDs := map[string]struct{}{}
	for id, source := range headSources {
		if before, ok := baseSources[id]; !ok {
			result.AddedSources = append(result.AddedSources, source)
			result.AddedSourceIDs = append(result.AddedSourceIDs, id)
			changedSourceIDs[id] = struct{}{}
		} else if fields := changedSourceFields(before, source); len(fields) > 0 {
			result.ChangedSources = append(result.ChangedSources, SnapshotRecordChange{ID: id, BeforeContentHash: before.ContentHash, AfterContentHash: source.ContentHash, ChangedFields: fields})
			result.ModifiedSourceIDs = append(result.ModifiedSourceIDs, id)
			changedSourceIDs[id] = struct{}{}
		}
	}
	for id, source := range baseSources {
		if _, ok := headSources[id]; !ok {
			result.RemovedSources = append(result.RemovedSources, source)
			result.RemovedSourceIDs = append(result.RemovedSourceIDs, id)
			changedSourceIDs[id] = struct{}{}
		}
	}
	diffChunks(&result, base.Chunks, head.Chunks, changedSourceIDs)
	diffLinks(&result, base.Links, head.Links)
	diffAliases(&result, base.Aliases, head.Aliases)
	diffSyncStatus(&result, base.SyncStatus, head.SyncStatus)
	result.ChangedSourceIDs = sortedKeys(changedSourceIDs)
	sort.Strings(result.AddedSourceIDs)
	sort.Strings(result.RemovedSourceIDs)
	sort.Strings(result.ModifiedSourceIDs)
	sortDiffResult(&result)
	return result
}

func diffChunks(result *DiffSnapshotResult, base, head []SnapshotChunk, changedSourceIDs map[string]struct{}) {
	baseMap := mapChunks(base)
	headMap := mapChunks(head)
	for id, chunk := range headMap {
		if before, ok := baseMap[id]; !ok {
			result.AddedChunks = append(result.AddedChunks, chunk)
			changedSourceIDs[chunk.SourceID] = struct{}{}
		} else if fields := changedChunkFields(before, chunk); len(fields) > 0 {
			result.ChangedChunks = append(result.ChangedChunks, SnapshotRecordChange{ID: id, BeforeContentHash: before.ContentHash, AfterContentHash: chunk.ContentHash, ChangedFields: fields})
			changedSourceIDs[chunk.SourceID] = struct{}{}
		}
	}
	for id, chunk := range baseMap {
		if _, ok := headMap[id]; !ok {
			result.RemovedChunks = append(result.RemovedChunks, chunk)
			changedSourceIDs[chunk.SourceID] = struct{}{}
		}
	}
}

func diffLinks(result *DiffSnapshotResult, base, head []SnapshotLink) {
	baseMap := mapLinks(base)
	headMap := mapLinks(head)
	for key, link := range headMap {
		if _, ok := baseMap[key]; !ok {
			result.AddedLinks = append(result.AddedLinks, link)
		}
	}
	for key, link := range baseMap {
		if _, ok := headMap[key]; !ok {
			result.RemovedLinks = append(result.RemovedLinks, link)
		}
	}
}

func diffAliases(result *DiffSnapshotResult, base, head []SnapshotAlias) {
	baseMap := mapAliases(base)
	headMap := mapAliases(head)
	for key, alias := range headMap {
		before, ok := baseMap[key]
		if !ok {
			result.ChangedAliases = append(result.ChangedAliases, SnapshotRecordChange{ID: key, ChangedFields: []string{"added"}})
			continue
		}
		fields := []string{}
		if before.RemoteKind != alias.RemoteKind {
			fields = append(fields, "remote_kind")
		}
		if before.RemoteID != alias.RemoteID {
			fields = append(fields, "remote_id")
		}
		if len(fields) > 0 {
			result.ChangedAliases = append(result.ChangedAliases, SnapshotRecordChange{ID: key, ChangedFields: fields})
		}
	}
	for key := range baseMap {
		if _, ok := headMap[key]; !ok {
			result.ChangedAliases = append(result.ChangedAliases, SnapshotRecordChange{ID: key, ChangedFields: []string{"removed"}})
		}
	}
}

func diffSyncStatus(result *DiffSnapshotResult, base, head []SnapshotSyncStatus) {
	baseMap := mapSync(base)
	headMap := mapSync(head)
	for id, status := range headMap {
		before, ok := baseMap[id]
		if !ok {
			result.ChangedSyncStatus = append(result.ChangedSyncStatus, SnapshotRecordChange{ID: id, AfterContentHash: status.RemoteRevision, ChangedFields: []string{"added"}})
			continue
		}
		fields := changedSyncFields(before, status)
		if len(fields) > 0 {
			result.ChangedSyncStatus = append(result.ChangedSyncStatus, SnapshotRecordChange{ID: id, BeforeContentHash: before.RemoteRevision, AfterContentHash: status.RemoteRevision, ChangedFields: fields})
		}
	}
	for id, status := range baseMap {
		if _, ok := headMap[id]; !ok {
			result.ChangedSyncStatus = append(result.ChangedSyncStatus, SnapshotRecordChange{ID: id, BeforeContentHash: status.RemoteRevision, ChangedFields: []string{"removed"}})
		}
	}
}

func mapSources(records []SnapshotSource) map[string]SnapshotSource {
	out := map[string]SnapshotSource{}
	for _, record := range records {
		out[record.ID] = record
	}
	return out
}

func mapChunks(records []SnapshotChunk) map[string]SnapshotChunk {
	out := map[string]SnapshotChunk{}
	for _, record := range records {
		out[record.ID] = record
	}
	return out
}

func mapLinks(records []SnapshotLink) map[string]SnapshotLink {
	out := map[string]SnapshotLink{}
	for _, record := range records {
		out[linkKey(record)] = record
	}
	return out
}

func mapAliases(records []SnapshotAlias) map[string]SnapshotAlias {
	out := map[string]SnapshotAlias{}
	for _, record := range records {
		out[aliasKey(record)] = record
	}
	return out
}

func mapSync(records []SnapshotSyncStatus) map[string]SnapshotSyncStatus {
	out := map[string]SnapshotSyncStatus{}
	for _, record := range records {
		out[record.SourceID] = record
	}
	return out
}

func changedSourceFields(a, b SnapshotSource) []string {
	fields := []string{}
	if a.Kind != b.Kind {
		fields = append(fields, "kind")
	}
	if a.Path != b.Path {
		fields = append(fields, "path")
	}
	if a.Title != b.Title {
		fields = append(fields, "title")
	}
	if a.Body != b.Body {
		fields = append(fields, "body")
	}
	if a.Status != b.Status {
		fields = append(fields, "status")
	}
	if strings.Join(sortedStrings(a.Labels), "\x00") != strings.Join(sortedStrings(b.Labels), "\x00") {
		fields = append(fields, "labels")
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		fields = append(fields, "created_at")
	}
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		fields = append(fields, "updated_at")
	}
	if a.ContentHash != b.ContentHash {
		fields = append(fields, "content_hash")
	}
	return fields
}

func changedChunkFields(a, b SnapshotChunk) []string {
	fields := []string{}
	if a.ContentHash != b.ContentHash {
		fields = append(fields, "content_hash")
	}
	if (a.ByteStart != b.ByteStart || a.ByteEnd != b.ByteEnd || a.LineStart != b.LineStart || a.LineEnd != b.LineEnd) && a.ContentHash == b.ContentHash {
		fields = append(fields, "citation_range_changed")
	} else {
		if a.ByteStart != b.ByteStart || a.ByteEnd != b.ByteEnd {
			fields = append(fields, "byte_range")
		}
		if a.LineStart != b.LineStart || a.LineEnd != b.LineEnd {
			fields = append(fields, "line_range")
		}
	}
	if strings.Join(a.HeadingPath, "\x00") != strings.Join(b.HeadingPath, "\x00") {
		fields = append(fields, "heading_path")
	}
	if a.SourceRevisionHash != b.SourceRevisionHash {
		fields = append(fields, "source_revision_hash")
	}
	if a.IndexBuildID != b.IndexBuildID {
		fields = append(fields, "index_build_id")
	}
	if a.Text != b.Text {
		fields = append(fields, "text")
	}
	if a.NormalizedText != b.NormalizedText {
		fields = append(fields, "normalized_text")
	}
	if stringMapKey(a.InheritedMetadata) != stringMapKey(b.InheritedMetadata) {
		fields = append(fields, "inherited_metadata")
	}
	if strings.Join(sortedStrings(a.OutboundLinks), "\x00") != strings.Join(sortedStrings(b.OutboundLinks), "\x00") {
		fields = append(fields, "outbound_links")
	}
	if stringMapKey(a.ResolvedAliases) != stringMapKey(b.ResolvedAliases) {
		fields = append(fields, "resolved_aliases")
	}
	return fields
}

func sortDiffResult(result *DiffSnapshotResult) {
	sort.SliceStable(result.AddedSources, func(i, j int) bool { return result.AddedSources[i].ID < result.AddedSources[j].ID })
	sort.SliceStable(result.RemovedSources, func(i, j int) bool { return result.RemovedSources[i].ID < result.RemovedSources[j].ID })
	sort.SliceStable(result.AddedChunks, func(i, j int) bool { return chunkKey(result.AddedChunks[i]) < chunkKey(result.AddedChunks[j]) })
	sort.SliceStable(result.RemovedChunks, func(i, j int) bool { return chunkKey(result.RemovedChunks[i]) < chunkKey(result.RemovedChunks[j]) })
	sort.SliceStable(result.AddedLinks, func(i, j int) bool { return linkKey(result.AddedLinks[i]) < linkKey(result.AddedLinks[j]) })
	sort.SliceStable(result.RemovedLinks, func(i, j int) bool { return linkKey(result.RemovedLinks[i]) < linkKey(result.RemovedLinks[j]) })
	sortRecordChanges(result.ChangedSources)
	sortRecordChanges(result.ChangedChunks)
	sortRecordChanges(result.ChangedAliases)
	sortRecordChanges(result.ChangedSyncStatus)
}

func sortRecordChanges(changes []SnapshotRecordChange) {
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	for i := range changes {
		sort.Strings(changes[i].ChangedFields)
	}
}

func chunkKey(chunk SnapshotChunk) string {
	return chunk.SourceID + "\x00" + chunk.ContentHash + "\x00" + fmt.Sprintf("%020d", chunk.ByteStart) + "\x00" + chunk.ID
}

func changedSyncFields(a, b SnapshotSyncStatus) []string {
	fields := []string{}
	if a.RemoteType != b.RemoteType {
		fields = append(fields, "remote_type")
	}
	if a.RemoteID != b.RemoteID {
		fields = append(fields, "remote_id")
	}
	if a.RemoteRevision != b.RemoteRevision {
		fields = append(fields, "remote_revision")
	}
	if a.Status != b.Status {
		fields = append(fields, "status")
	}
	if a.Freshness != b.Freshness {
		fields = append(fields, "freshness")
	}
	if !a.LastFetchedAt.Equal(b.LastFetchedAt) {
		fields = append(fields, "last_fetched_at")
	}
	return fields
}

func linkKey(link SnapshotLink) string {
	return link.SourceID + "\x00" + link.TargetID + "\x00" + link.LinkType + "\x00" + link.Text
}

func aliasKey(alias SnapshotAlias) string {
	return alias.SourceID + "\x00" + alias.AliasKind + "\x00" + alias.AliasValue
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func stringMapKey(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"\x00"+values[key])
	}
	return strings.Join(parts, "\x01")
}

func snapshotCreatedAt(snapshot Snapshot) time.Time {
	var latest time.Time
	for _, source := range snapshot.Sources {
		if source.CreatedAt.After(latest) {
			latest = source.CreatedAt
		}
		if source.UpdatedAt.After(latest) {
			latest = source.UpdatedAt
		}
	}
	for _, status := range snapshot.SyncStatus {
		if status.LastFetchedAt.After(latest) {
			latest = status.LastFetchedAt
		}
	}
	if latest.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return latest.UTC()
}

func formatSnapshotTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
