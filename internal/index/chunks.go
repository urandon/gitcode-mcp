package index

import (
	"strconv"
	"strings"
)

func ChunkSource(source SourceRecord, parsed ParsedSource) []Chunk {
	body := source.Body
	if body == "" {
		return nil
	}
	boundaries := []int{0}
	for _, heading := range parsed.Headings {
		if heading.ByteStart > 0 {
			boundaries = append(boundaries, heading.ByteStart)
		}
	}
	boundaries = append(boundaries, len(body))
	boundaries = uniqueInts(boundaries)
	var chunks []Chunk
	for i := 0; i < len(boundaries)-1; i++ {
		start, end := boundaries[i], boundaries[i+1]
		if start >= end {
			continue
		}
		text := body[start:end]
		normalized := strings.ReplaceAll(strings.ReplaceAll(strings.ToValidUTF8(text, "�"), "\r\n", "\n"), "\r", "\n")
		lineStart := lineForOffset(parsed.LineStarts, start)
		lineEnd := lineForOffset(parsed.LineStarts, maxInt(start, end-1))
		path := headingAtLine(parsed.Headings, lineStart)
		outbound, resolved := linksInRange(parsed.Links, start, end)
		chunk := Chunk{SourceID: source.ID, ContentHash: parsed.ContentHash, ByteStart: start, ByteEnd: end, LineStart: lineStart, LineEnd: lineEnd, HeadingPath: path, Text: text, NormalizedText: normalized, InheritedMetadata: inheritedMetadata(source, parsed), OutboundLinks: outbound, ResolvedAliases: resolved}
		chunk.ID = stableHash(source.ID, parsed.ContentHash, strconv.Itoa(start))
		chunk.CitationAnchorID = anchorID(source.ID, parsed.ContentHash, start, "chunk")
		chunks = append(chunks, chunk)
	}
	return chunks
}

func inheritedMetadata(source SourceRecord, parsed ParsedSource) map[string]string {
	metadata := mergeMetadata(source.Metadata, parsed.Frontmatter.Values)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["source_id"] = source.ID
	metadata["kind"] = source.Kind
	metadata["path"] = source.Path
	return metadata
}

func linksInRange(links []Link, start, end int) ([]string, map[string]string) {
	var outbound []string
	resolved := map[string]string{}
	for _, link := range links {
		if link.ByteStart >= start && link.ByteStart < end {
			outbound = append(outbound, link.Target)
			resolved[link.Raw] = link.Target
		}
	}
	return outbound, resolved
}

func uniqueInts(values []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
