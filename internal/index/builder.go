package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

type Builder struct {
	options ChunkOptions
}

type previousIndexStateReader interface {
	ListIndexStates(context.Context, ChunkQuery) ([]IndexState, error)
}

func NewBuilder(options ChunkOptions) Builder {
	return Builder{options: normalizeChunkOptions(options)}
}

func FullBuild(ctx context.Context, reader SourceReader, writer DerivedWriter) (BuildReport, error) {
	return NewBuilder(ChunkOptions{}).FullBuild(ctx, reader, writer)
}

func FullBuildWithOptions(ctx context.Context, reader SourceReader, writer DerivedWriter, options ChunkOptions) (BuildReport, error) {
	return NewBuilder(options).FullBuild(ctx, reader, writer)
}

func IncrementalBuild(ctx context.Context, reader SourceReader, writer DerivedWriter) (BuildReport, error) {
	return NewBuilder(ChunkOptions{}).IncrementalBuild(ctx, reader, writer)
}

func IncrementalBuildWithOptions(ctx context.Context, reader SourceReader, writer DerivedWriter, options ChunkOptions) (BuildReport, error) {
	return NewBuilder(options).IncrementalBuild(ctx, reader, writer)
}

func (b Builder) FullBuild(ctx context.Context, reader SourceReader, writer DerivedWriter) (BuildReport, error) {
	return b.build(ctx, reader, writer, false)
}

func (b Builder) IncrementalBuild(ctx context.Context, reader SourceReader, writer DerivedWriter) (BuildReport, error) {
	return b.build(ctx, reader, writer, true)
}

func (b Builder) build(ctx context.Context, reader SourceReader, writer DerivedWriter, incremental bool) (BuildReport, error) {
	sources, err := reader.ListSources(ctx)
	if err != nil {
		return BuildReport{}, err
	}
	aliasIndex, diagnostics, collidingSources := buildAliasIndex(sources)
	if len(diagnostics) > 0 {
		if err := writer.WriteDiagnostics(ctx, diagnostics); err != nil {
			return BuildReport{}, err
		}
	}
	previousHashes := map[string]string{}
	if incremental {
		previousHashes = previousIndexStateHashes(ctx, writer, b.options.Policy)
	}
	report := BuildReport{Diagnostics: diagnostics, CollisionCount: len(diagnostics)}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		contentHash := ContentHash(source.Body)
		if incremental && previousHashes[indexStateHashKey(source.RepoID, source.ID, sourceRecordID(source), source.SnapshotID, b.options.Policy)] == contentHash {
			report.SkippedCount++
			continue
		}
		parsed := ParseSource(source)
		parsed.Diagnostics = append(parsed.Diagnostics, diagnosticsForSource(diagnostics, source.ID)...)
		if collidingSources[source.ID] {
			report.ProcessedCount++
			report.Diagnostics = append(report.Diagnostics, parsed.Diagnostics...)
			continue
		}
		derived := b.deriveSource(source, parsed, aliasIndex)
		if err := writer.ReplaceSourceDerived(ctx, derived); err != nil {
			return report, err
		}
		report.ProcessedCount++
		report.RewrittenRowCount += derived.RewrittenRowCount
		report.BrokenLinkCount += len(derived.BrokenLinks)
		report.Diagnostics = append(report.Diagnostics, parsed.Diagnostics...)
	}
	return report, nil
}

func previousIndexStateHashes(ctx context.Context, reader any, policy ChunkPolicy) map[string]string {
	stateReader, ok := reader.(previousIndexStateReader)
	if !ok {
		return map[string]string{}
	}
	states, err := stateReader.ListIndexStates(ctx, ChunkQuery{Policy: policy})
	if err != nil {
		return map[string]string{}
	}
	hashes := map[string]string{}
	for _, state := range states {
		hashes[indexStateHashKey(state.RepoID, state.SourceID, state.RecordID, state.SnapshotID, state.Policy)] = state.ContentHash
	}
	return hashes
}

func indexStateHashKey(repoID, sourceID, recordID, snapshotID string, policy ChunkPolicy) string {
	if policy == "" {
		policy = ChunkPolicyHeading
	}
	return repoID + "\x00" + sourceID + "\x00" + recordID + "\x00" + snapshotID + "\x00" + string(policy)
}

func (b Builder) deriveSource(source SourceRecord, parsed ParsedSource, aliasIndex map[string][]string) SourceDerived {
	anchors := BuildCitationAnchors(source, parsed)
	chunks := ChunkSourceWithOptions(source, parsed, b.options)
	anchorByStart := map[int]string{}
	for _, anchor := range anchors {
		anchorByStart[anchor.ByteStart] = anchor.ID
	}
	var links []DerivedLink
	var backlinks []BacklinkRow
	var broken []BrokenLink
	for _, link := range parsed.Links {
		key := aliasKey("id", link.Target)
		matches := aliasIndex[key]
		if len(matches) == 0 && strings.HasPrefix(link.Target, "#") {
			matches = []string{source.ID}
		}
		if len(matches) == 0 && strings.Contains(link.Target, ".md") {
			broken = append(broken, brokenLink(source, link, "unresolved_relative_path"))
			continue
		}
		if len(matches) == 0 {
			broken = append(broken, brokenLink(source, link, "unresolved_alias"))
			continue
		}
		if len(matches) > 1 {
			broken = append(broken, brokenLink(source, link, "ambiguous_alias"))
			continue
		}
		anchorID := anchorByStart[link.ByteStart]
		derived := DerivedLink{SourceID: source.ID, TargetID: matches[0], RawTarget: link.Target, Text: link.Text, Kind: link.Kind, LineStart: link.LineStart, LineEnd: link.LineEnd, SourceHash: parsed.ContentHash, AnchorID: anchorID}
		links = append(links, derived)
		backlinks = append(backlinks, BacklinkRow{SourceID: source.ID, TargetID: matches[0], Text: link.Text, Kind: link.Kind, LineStart: link.LineStart, LineEnd: link.LineEnd})
	}
	for i := range chunks {
		if chunks[i].CitationAnchorID == "" {
			chunks[i].CitationAnchorID = anchorByStart[chunks[i].ByteStart]
		}
	}
	ledger := []SourceLedgerRow{{SourceID: source.ID, Kind: source.Kind, Path: source.Path, Title: source.Title, Status: source.Status, ContentHash: parsed.ContentHash}}
	return SourceDerived{
		SourceID:          source.ID,
		ContentHash:       parsed.ContentHash,
		Links:             links,
		Backlinks:         backlinks,
		BrokenLinks:       broken,
		CitationAnchors:   anchors,
		Chunks:            chunks,
		SourceLedgerRows:  ledger,
		TaskBacklogRows:   taskRows(source, parsed, anchors),
		TrackRows:         trackRows(source, parsed, anchors),
		AcceptanceRows:    acceptanceRows(source, parsed, anchors),
		OpenQuestionRows:  openQuestionRows(source, parsed, anchors),
		IndexState:        IndexState{RepoID: source.RepoID, SourceID: source.ID, RecordID: sourceRecordID(source), SnapshotID: source.SnapshotID, ContentHash: parsed.ContentHash, RemoteRevision: source.RemoteRevision, SyncRevision: source.SyncRevision, SyncEventID: source.SyncEventID, SourceUpdatedAt: source.UpdatedAt.UTC(), Policy: b.options.Policy, IndexedAt: time.Now().UTC(), ChunkCount: len(chunks), CitationCount: len(anchors)},
		Diagnostics:       parsed.Diagnostics,
		RewrittenRowCount: 1 + len(links) + len(broken) + len(anchors) + len(chunks),
	}
}

func buildAliasIndex(sources []SourceRecord) (map[string][]string, []CollisionDiagnostic, map[string]bool) {
	index := map[string][]string{}
	seenID := map[string]string{}
	dedup := map[string]map[string]bool{}
	for _, source := range sources {
		ids := append([]Alias{{Type: "id", ID: source.ID}}, source.Aliases...)
		ids = append(ids, source.RemoteAliases...)
		for _, alias := range ids {
			if alias.ID == "" {
				continue
			}
			key := aliasKey(alias.Type, alias.ID)
			if dedup[key] == nil {
				dedup[key] = map[string]bool{}
			}
			if dedup[key][source.ID] {
				continue
			}
			dedup[key][source.ID] = true
			index[key] = append(index[key], source.ID)
			if alias.Type == "id" {
				if existing := seenID[alias.ID]; existing != "" && existing != source.ID {
					index[key] = uniqueStrings(append(index[key], existing, source.ID))
				} else {
					seenID[alias.ID] = source.ID
				}
			}
		}
	}
	var diagnostics []CollisionDiagnostic
	colliding := map[string]bool{}
	for key, matches := range index {
		matches = uniqueStrings(matches)
		index[key] = matches
		if len(matches) > 1 {
			kind := "ambiguous_alias"
			if strings.HasPrefix(key, "id:") {
				kind = "duplicate_stable_id"
			}
			for _, sourceID := range matches {
				diagnostics = append(diagnostics, CollisionDiagnostic{SourceID: sourceID, Kind: kind, Key: strings.TrimPrefix(key, "id:"), Message: key + " maps to multiple sources"})
				if kind == "duplicate_stable_id" {
					colliding[sourceID] = true
				}
			}
		}
	}
	return index, diagnostics, colliding
}

func diagnosticsForSource(all []CollisionDiagnostic, sourceID string) []CollisionDiagnostic {
	var out []CollisionDiagnostic
	for _, diagnostic := range all {
		if diagnostic.SourceID == sourceID {
			out = append(out, diagnostic)
		}
	}
	return out
}

func ContentHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func stableHash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func aliasKey(aliasType, id string) string {
	if aliasType == "" {
		aliasType = "id"
	}
	return aliasType + ":" + id
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func brokenLink(source SourceRecord, link Link, reason string) BrokenLink {
	return BrokenLink{SourceID: source.ID, SourcePath: source.Path, RawTarget: link.Target, Text: link.Text, Reason: reason, LineStart: link.LineStart, LineEnd: link.LineEnd}
}
