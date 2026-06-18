package index

import "strings"

func taskRows(source SourceRecord, parsed ParsedSource, anchors []CitationAnchor) []TaskBacklogRow {
	if source.Kind != "task" && !strings.HasPrefix(source.ID, "TASK-") {
		return nil
	}
	status := source.Status
	line := 1
	anchorID := firstAnchorID(anchors, "task_status")
	if len(parsed.Statuses) > 0 {
		status = parsed.Statuses[0].Value
		line = parsed.Statuses[0].LineStart
	}
	return []TaskBacklogRow{{SourceID: source.ID, Title: source.Title, Status: status, LineStart: line, AnchorID: anchorID}}
}

func trackRows(source SourceRecord, parsed ParsedSource, anchors []CitationAnchor) []TrackRow {
	metadata := mergeMetadata(source.Metadata, parsed.Frontmatter.Values)
	track := metadata["track"]
	if track == "" {
		track = metadata["component"]
	}
	if track == "" {
		return nil
	}
	return []TrackRow{{SourceID: source.ID, Track: track, Status: source.Status, AnchorID: firstAnchorID(anchors, "heading")}}
}

func acceptanceRows(source SourceRecord, parsed ParsedSource, anchors []CitationAnchor) []AcceptanceRow {
	var rows []AcceptanceRow
	for _, anchor := range anchors {
		if anchor.Kind == "acceptance" {
			rows = append(rows, AcceptanceRow{SourceID: source.ID, Title: anchor.Title, LineStart: anchor.LineStart, LineEnd: anchor.LineEnd, AnchorID: anchor.ID})
		}
	}
	return rows
}

func openQuestionRows(source SourceRecord, parsed ParsedSource, anchors []CitationAnchor) []OpenQuestionRow {
	var rows []OpenQuestionRow
	for _, anchor := range anchors {
		if anchor.Kind == "open_question" {
			rows = append(rows, OpenQuestionRow{SourceID: source.ID, Question: anchor.Title, LineStart: anchor.LineStart, LineEnd: anchor.LineEnd, AnchorID: anchor.ID})
		}
	}
	for _, link := range parsed.Links {
		if strings.Contains(strings.ToLower(link.Text), "question") {
			rows = append(rows, OpenQuestionRow{SourceID: source.ID, Question: link.Text, LineStart: link.LineStart, LineEnd: link.LineEnd, AnchorID: firstAnchorID(anchors, "link_target")})
		}
	}
	return rows
}

func firstAnchorID(anchors []CitationAnchor, kind string) string {
	for _, anchor := range anchors {
		if anchor.Kind == kind {
			return anchor.ID
		}
	}
	return ""
}
