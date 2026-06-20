package index

import (
	"strconv"
	"strings"
)

func BuildCitationAnchors(source SourceRecord, parsed ParsedSource) []CitationAnchor {
	var anchors []CitationAnchor
	base := CitationAnchor{RepoID: source.RepoID, SourceID: source.ID, RecordID: sourceRecordID(source), SnapshotID: source.SnapshotID, ContentHash: parsed.ContentHash}
	for _, heading := range parsed.Headings {
		anchor := base
		anchor.ID = anchorID(source.ID, parsed.ContentHash, heading.ByteStart, "heading")
		anchor.ByteStart = heading.ByteStart
		anchor.ByteEnd = heading.ByteEnd
		anchor.LineStart = heading.LineStart
		anchor.LineEnd = heading.LineEnd
		anchor.HeadingPath = heading.HeadingPath
		anchor.Kind = "heading"
		anchor.Title = heading.Title
		anchors = append(anchors, anchor)
	}
	for _, status := range parsed.Statuses {
		anchor := base
		anchor.ID = anchorID(source.ID, parsed.ContentHash, status.ByteStart, "task_status")
		anchor.ByteStart = status.ByteStart
		anchor.ByteEnd = status.ByteEnd
		anchor.LineStart = status.LineStart
		anchor.LineEnd = status.LineEnd
		anchor.HeadingPath = status.HeadingPath
		anchor.Kind = "task_status"
		anchor.Title = status.Value
		anchor.DerivedRowID = source.ID
		anchors = append(anchors, anchor)
	}
	for _, link := range parsed.Links {
		anchor := base
		anchor.ID = anchorID(source.ID, parsed.ContentHash, link.ByteStart, "link_target")
		anchor.ByteStart = link.ByteStart
		anchor.ByteEnd = link.ByteEnd
		anchor.LineStart = link.LineStart
		anchor.LineEnd = link.LineEnd
		anchor.HeadingPath = headingAtLine(parsed.Headings, link.LineStart)
		anchor.Kind = "link_target"
		anchor.Title = link.Text
		anchors = append(anchors, anchor)
	}
	for _, heading := range parsed.Headings {
		lower := strings.ToLower(heading.Title)
		if strings.Contains(lower, "acceptance") {
			anchor := base
			anchor.ID = anchorID(source.ID, parsed.ContentHash, heading.ByteStart, "acceptance")
			anchor.ByteStart = heading.ByteStart
			anchor.ByteEnd = heading.ByteEnd
			anchor.LineStart = heading.LineStart
			anchor.LineEnd = heading.LineEnd
			anchor.HeadingPath = heading.HeadingPath
			anchor.Kind = "acceptance"
			anchor.Title = heading.Title
			anchor.DerivedRowID = source.ID + ":acceptance"
			anchors = append(anchors, anchor)
		}
		if strings.Contains(lower, "question") {
			anchor := base
			anchor.ID = anchorID(source.ID, parsed.ContentHash, heading.ByteStart, "open_question")
			anchor.ByteStart = heading.ByteStart
			anchor.ByteEnd = heading.ByteEnd
			anchor.LineStart = heading.LineStart
			anchor.LineEnd = heading.LineEnd
			anchor.HeadingPath = heading.HeadingPath
			anchor.Kind = "open_question"
			anchor.Title = heading.Title
			anchor.DerivedRowID = source.ID + ":question"
			anchors = append(anchors, anchor)
		}
	}
	for _, chunk := range ChunkSource(source, parsed) {
		anchor := base
		anchor.ID = chunk.CitationAnchorID
		anchor.RecordID = chunk.RecordID
		anchor.SnapshotID = chunk.SnapshotID
		anchor.Policy = chunk.Policy
		anchor.ByteStart = chunk.ByteStart
		anchor.ByteEnd = chunk.ByteEnd
		anchor.LineStart = chunk.LineStart
		anchor.LineEnd = chunk.LineEnd
		anchor.HeadingPath = chunk.HeadingPath
		anchor.Kind = "chunk"
		anchor.Title = "chunk " + strconv.Itoa(chunk.LineStart)
		anchors = append(anchors, anchor)
	}
	return dedupeAnchors(anchors)
}

func anchorID(sourceID, contentHash string, byteStart int, kind string) string {
	return stableHash(sourceID, contentHash, strconv.Itoa(byteStart), kind)
}

func headingAtLine(headings []Heading, line int) []string {
	var path []string
	for _, heading := range headings {
		if heading.LineStart <= line {
			path = heading.HeadingPath
		}
	}
	return append([]string(nil), path...)
}

func dedupeAnchors(anchors []CitationAnchor) []CitationAnchor {
	seen := map[string]bool{}
	var out []CitationAnchor
	for _, anchor := range anchors {
		if anchor.ID == "" || seen[anchor.ID] {
			continue
		}
		seen[anchor.ID] = true
		out = append(out, anchor)
	}
	return out
}
