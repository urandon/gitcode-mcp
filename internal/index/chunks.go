package index

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const chunkSoftLimitBytes = 4 * 1024

func ChunkSource(source SourceRecord, parsed ParsedSource) []Chunk {
	return ChunkSourceWithOptions(source, parsed, ChunkOptions{})
}

func ChunkSourceWithOptions(source SourceRecord, parsed ParsedSource, options ChunkOptions) []Chunk {
	options = normalizeChunkOptions(options)
	body := source.Body
	if body == "" {
		return nil
	}
	blocks := chunkBlocksWithOptions(body, parsed, options)
	var chunks []Chunk
	for _, block := range blocks {
		if block.start >= block.end {
			continue
		}
		text := body[block.start:block.end]
		normalizedText := normalizeChunkText(text)
		if normalizedText == "" {
			continue
		}
		outbound, resolved := linksInRange(parsed.Links, parsed.StableIDs, block.start, block.end)
		chunk := Chunk{
			RepoID:            source.RepoID,
			SourceID:          source.ID,
			RecordID:          sourceRecordID(source),
			SnapshotID:        source.SnapshotID,
			ContentHash:       parsed.ContentHash,
			ByteStart:         block.start,
			ByteEnd:           block.end,
			LineStart:         lineForOffset(parsed.LineStarts, block.start),
			LineEnd:           lineForOffset(parsed.LineStarts, maxInt(block.start, block.end-1)),
			HeadingPath:       headingAtLine(parsed.Headings, lineForOffset(parsed.LineStarts, block.start)),
			Text:              text,
			NormalizedText:    normalizedText,
			InheritedMetadata: inheritedMetadata(source, parsed),
			OutboundLinks:     uniqueStrings(outbound),
			ResolvedAliases:   resolved,
			Policy:            options.Policy,
		}
		chunk.ID = chunkIDWithOptions(source.ID, parsed.ContentHash, block.start, source.SnapshotID, options)
		chunk.CitationAnchorID = anchorID(source.ID, parsed.ContentHash, block.start, "chunk")
		chunks = append(chunks, chunk)
	}
	return chunks
}

type chunkBlock struct {
	start int
	end   int
}

func normalizeChunkOptions(options ChunkOptions) ChunkOptions {
	if options.Policy == "" {
		options.Policy = ChunkPolicyHeading
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = chunkSoftLimitBytes
	}
	if options.WindowBytes <= 0 {
		options.WindowBytes = options.MaxBytes
	}
	if options.OverlapBytes < 0 {
		options.OverlapBytes = 0
	}
	if options.OverlapBytes >= options.WindowBytes {
		options.OverlapBytes = options.WindowBytes / 4
	}
	return options
}

func chunkBlocksWithOptions(body string, parsed ParsedSource, options ChunkOptions) []chunkBlock {
	if options.Policy == ChunkPolicySlidingWindow {
		return slidingWindowBlocks(body, parsed, options)
	}
	return headingBlocks(body, parsed, options.MaxBytes)
}

func chunkBlocks(body string, parsed ParsedSource) []chunkBlock {
	return headingBlocks(body, parsed, chunkSoftLimitBytes)
}

func headingBlocks(body string, parsed ParsedSource, maxBytes int) []chunkBlock {
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
		if !inFence && line.start > chunkStart && line.end-chunkStart > maxBytes {
			blocks = append(blocks, splitOversizedBlock(lines, chunkStart, chunkEnd, maxBytes)...)
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
		blocks = append(blocks, splitOversizedBlock(lines, chunkStart, chunkEnd, maxBytes)...)
	}
	return blocks
}

func splitOversizedBlock(lines []sourceLine, start, end, maxBytes int) []chunkBlock {
	if maxBytes <= 0 || end-start <= maxBytes {
		return []chunkBlock{{start: start, end: end}}
	}
	var blocks []chunkBlock
	blockStart := start
	blockEnd := start
	inFence := false
	lastParagraphEnd := -1
	for _, line := range lines {
		if line.end <= start || line.start >= end {
			continue
		}
		if fenceLine(line.text) {
			inFence = !inFence
		}
		if strings.TrimSpace(line.text) == "" && !inFence {
			lastParagraphEnd = line.end
		}
		if line.end-blockStart > maxBytes && line.start > blockStart {
			splitEnd := blockEnd
			if lastParagraphEnd > blockStart && lastParagraphEnd < line.end {
				splitEnd = lastParagraphEnd
			}
			blocks = append(blocks, chunkBlock{start: blockStart, end: splitEnd})
			blockStart = splitEnd
		}
		blockEnd = line.end
	}
	if blockStart < end {
		blocks = append(blocks, chunkBlock{start: blockStart, end: end})
	}
	return blocks
}

func slidingWindowBlocks(body string, parsed ParsedSource, options ChunkOptions) []chunkBlock {
	lines := sourceLines(body)
	startLine := 0
	if parsed.Frontmatter.Valid && parsed.FrontmatterEnd > 0 {
		startLine = lineIndexForOffset(lines, parsed.FrontmatterEnd)
	}
	var blocks []chunkBlock
	for i := startLine; i < len(lines); {
		start := lines[i].start
		end := lines[i].end
		j := i + 1
		for j < len(lines) && lines[j].end-start <= options.WindowBytes {
			end = lines[j].end
			j++
		}
		blocks = append(blocks, chunkBlock{start: start, end: end})
		if j >= len(lines) {
			break
		}
		nextStart := maxInt(start+1, end-options.OverlapBytes)
		next := j
		for next > startLine && lines[next-1].start >= nextStart {
			next--
		}
		if next <= i {
			next = i + 1
		}
		i = next
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
	if source.RepoID != "" {
		metadata["repo_id"] = source.RepoID
	}
	if recordID := sourceRecordID(source); recordID != "" {
		metadata["record_id"] = recordID
	}
	if source.SnapshotID != "" {
		metadata["snapshot_id"] = source.SnapshotID
	}
	if source.RemoteRevision != "" {
		metadata["remote_revision"] = source.RemoteRevision
	}
	if source.SyncRevision != "" {
		metadata["sync_revision"] = source.SyncRevision
	}
	if source.SyncEventID != "" {
		metadata["sync_event_id"] = source.SyncEventID
	}
	if !source.UpdatedAt.IsZero() {
		metadata["source_updated_at"] = source.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return metadata
}

func sourceRecordID(source SourceRecord) string {
	if source.RecordID != "" {
		return source.RecordID
	}
	return source.ID
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
	return chunkIDWithOptions(sourceID, contentHash, byteStart, "", ChunkOptions{})
}

func chunkIDWithOptions(sourceID, contentHash string, byteStart int, snapshotID string, options ChunkOptions) string {
	options = normalizeChunkOptions(options)
	sum := sha256.Sum256([]byte(strings.Join([]string{sourceID, contentHash, string(options.Policy), strconv.Itoa(byteStart), strconv.Itoa(options.MaxBytes), strconv.Itoa(options.WindowBytes), strconv.Itoa(options.OverlapBytes), snapshotID}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
