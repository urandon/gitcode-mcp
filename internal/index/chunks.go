package index

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
)

const chunkSoftLimitBytes = 4 * 1024

func ChunkSource(source SourceRecord, parsed ParsedSource) []Chunk {
	body := source.Body
	if body == "" {
		return nil
	}
	blocks := chunkBlocks(body, parsed)
	var chunks []Chunk
	for _, block := range blocks {
		if block.start >= block.end {
			continue
		}
		text := body[block.start:block.end]
		outbound, resolved := linksInRange(parsed.Links, parsed.StableIDs, block.start, block.end)
		chunk := Chunk{
			SourceID:          source.ID,
			ContentHash:       parsed.ContentHash,
			ByteStart:         block.start,
			ByteEnd:           block.end,
			LineStart:         lineForOffset(parsed.LineStarts, block.start),
			LineEnd:           lineForOffset(parsed.LineStarts, maxInt(block.start, block.end-1)),
			HeadingPath:       headingAtLine(parsed.Headings, lineForOffset(parsed.LineStarts, block.start)),
			Text:              text,
			NormalizedText:    normalizeChunkText(text),
			InheritedMetadata: inheritedMetadata(source, parsed),
			OutboundLinks:     uniqueStrings(outbound),
			ResolvedAliases:   resolved,
		}
		chunk.ID = chunkID(source.ID, parsed.ContentHash, block.start)
		chunk.CitationAnchorID = anchorID(source.ID, parsed.ContentHash, block.start, "chunk")
		chunks = append(chunks, chunk)
	}
	return chunks
}

type chunkBlock struct {
	start int
	end   int
}

func chunkBlocks(body string, parsed ParsedSource) []chunkBlock {
	lines := sourceLines(body)
	startLine := 0
	if parsed.Frontmatter.Valid && parsed.FrontmatterEnd > 0 {
		startLine = lineIndexForOffset(lines, parsed.FrontmatterEnd)
	}
	var blocks []chunkBlock
	chunkStart := -1
	chunkEnd := -1
	inFence := false
	for i := startLine; i < len(lines); i++ {
		line := lines[i]
		if chunkStart < 0 {
			chunkStart = line.start
			chunkEnd = line.end
		}
		isHeading := atxHeadingLine(line.text)
		if isHeading && chunkStart >= 0 && line.start > chunkStart && !inFence {
			blocks = append(blocks, chunkBlock{start: chunkStart, end: chunkEnd})
			chunkStart = line.start
		}
		lineStartsFence := fenceLine(line.text)
		lineEndsFence := lineStartsFence && inFence
		if !inFence && line.start > chunkStart && line.end-chunkStart > chunkSoftLimitBytes {
			blocks = append(blocks, chunkBlock{start: chunkStart, end: chunkEnd})
			chunkStart = line.start
		}
		chunkEnd = line.end
		if lineStartsFence {
			if lineEndsFence {
				inFence = false
			} else {
				inFence = true
			}
		}
	}
	if chunkStart >= 0 && chunkStart < chunkEnd {
		blocks = append(blocks, chunkBlock{start: chunkStart, end: chunkEnd})
	}
	return blocks
}

type sourceLine struct {
	start int
	end   int
	text  string
}

func sourceLines(body string) []sourceLine {
	var lines []sourceLine
	start := 0
	for i := 0; i < len(body); i++ {
		if body[i] == '\n' {
			lines = append(lines, sourceLine{start: start, end: i + 1, text: strings.TrimSuffix(body[start:i+1], "\n")})
			start = i + 1
		}
	}
	if start < len(body) {
		lines = append(lines, sourceLine{start: start, end: len(body), text: body[start:]})
	}
	return lines
}

func lineIndexForOffset(lines []sourceLine, offset int) int {
	for i, line := range lines {
		if line.end > offset {
			return i
		}
	}
	return len(lines)
}

func atxHeadingLine(line string) bool {
	return headingRe.MatchString(strings.TrimSuffix(line, "\r"))
}

func fenceLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
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

func linksInRange(links []Link, stableIDs []StableID, start, end int) ([]string, map[string]string) {
	var outbound []string
	resolved := map[string]string{}
	for _, link := range links {
		if link.ByteStart >= start && link.ByteStart < end {
			outbound = append(outbound, link.Target)
			resolved[link.Raw] = link.Target
		}
	}
	for _, id := range stableIDs {
		if id.ByteStart >= start && id.ByteStart < end {
			outbound = append(outbound, id.ID)
			resolved[id.ID] = id.ID
		}
	}
	return outbound, resolved
}

func normalizeChunkText(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var b strings.Builder
	inFence := false
	space := false
	for _, line := range strings.SplitAfter(text, "\n") {
		if fenceLine(strings.TrimSuffix(line, "\n")) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
				space = false
			}
			b.WriteString(lowerASCII(line))
			inFence = !inFence
			continue
		}
		if inFence {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
				space = false
			}
			b.WriteString(lowerASCII(line))
			continue
		}
		for _, r := range line {
			if unicode.IsSpace(r) {
				space = true
				continue
			}
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func lowerASCII(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func chunkID(sourceID, contentHash string, byteStart int) string {
	sum := sha256.Sum256([]byte(sourceID + "\x00" + contentHash + "\x00" + strconv.Itoa(byteStart)))
	return hex.EncodeToString(sum[:])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
