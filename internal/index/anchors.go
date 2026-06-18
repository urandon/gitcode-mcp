package index

import (
	"strconv"
	"strings"
)

func BuildCitationAnchors(source SourceRecord, parsed ParsedSource) []CitationAnchor {
	var anchors []CitationAnchor
	for _, heading := range parsed.Headings {
		anchors = append(anchors, CitationAnchor{ID: anchorID(source.ID, parsed.ContentHash, heading.ByteStart, "heading"), SourceID: source.ID, ContentHash: parsed.ContentHash, ByteStart: heading.ByteStart, ByteEnd: heading.ByteEnd, LineStart: heading.LineStart, LineEnd: heading.LineEnd, HeadingPath: heading.HeadingPath, Kind: "heading", Title: heading.Title})
	}
	for _, status := range parsed.Statuses {
		anchors = append(anchors, CitationAnchor{ID: anchorID(source.ID, parsed.ContentHash, status.ByteStart, "task_status"), SourceID: source.ID, ContentHash: parsed.ContentHash, ByteStart: status.ByteStart, ByteEnd: status.ByteEnd, LineStart: status.LineStart, LineEnd: status.LineEnd, HeadingPath: status.HeadingPath, Kind: "task_status", Title: status.Value, DerivedRowID: source.ID})
	}
	for _, link := range parsed.Links {
		anchors = append(anchors, CitationAnchor{ID: anchorID(source.ID, parsed.ContentHash, link.ByteStart, "link_target"), SourceID: source.ID, ContentHash: parsed.ContentHash, ByteStart: link.ByteStart, ByteEnd: link.ByteEnd, LineStart: link.LineStart, LineEnd: link.LineEnd, HeadingPath: headingAtLine(parsed.Headings, link.LineStart), Kind: "link_target", Title: link.Text})
	}
	for _, heading := range parsed.Headings {
		lower := strings.ToLower(heading.Title)
		if strings.Contains(lower, "acceptance") {
			anchors = append(anchors, CitationAnchor{ID: anchorID(source.ID, parsed.ContentHash, heading.ByteStart, "acceptance"), SourceID: source.ID, ContentHash: parsed.ContentHash, ByteStart: heading.ByteStart, ByteEnd: heading.ByteEnd, LineStart: heading.LineStart, LineEnd: heading.LineEnd, HeadingPath: heading.HeadingPath, Kind: "acceptance", Title: heading.Title, DerivedRowID: source.ID + ":acceptance"})
		}
		if strings.Contains(lower, "question") {
			anchors = append(anchors, CitationAnchor{ID: anchorID(source.ID, parsed.ContentHash, heading.ByteStart, "open_question"), SourceID: source.ID, ContentHash: parsed.ContentHash, ByteStart: heading.ByteStart, ByteEnd: heading.ByteEnd, LineStart: heading.LineStart, LineEnd: heading.LineEnd, HeadingPath: heading.HeadingPath, Kind: "open_question", Title: heading.Title, DerivedRowID: source.ID + ":question"})
		}
	}
	for _, chunk := range ChunkSource(source, parsed) {
		anchors = append(anchors, CitationAnchor{ID: chunk.CitationAnchorID, SourceID: source.ID, ContentHash: parsed.ContentHash, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPath: chunk.HeadingPath, Kind: "chunk", Title: "chunk " + strconv.Itoa(chunk.LineStart)})
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
