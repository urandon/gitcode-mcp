package localauthority

import (
	"context"
	"strings"
	"unicode/utf8"
)

// Input is transient and deliberately unexported: JSON cannot persist source
// bytes by accidentally marshaling the producer's working set.
type semanticChunk struct {
	record semanticRecord
	input  string
}

// Spans are offsets in typed searchable text, not raw Git markdown lines.
// LineEnd is the line containing the final byte (a newline belongs to the line
// it terminates). Empty/archived/tombstoned sources have no eligible chunks.
func captureSemanticChunks(ctx context.Context, entry SnapshotEntity, sequence uint64) ([]semanticChunk, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	e, err := projectedEntry(entry)
	if err != nil || !e.valid(entry.Entity.Citation.ProjectID, sequence) {
		return nil, 0, Error{ProjectionCorrupt}
	}
	chunks := []semanticChunk{}
	if e.State != "active" {
		return chunks, 0, nil
	}
	text, err := searchableText(entry.Entity)
	if err != nil || len(text) > semanticTextBytes || !utf8.ValidString(text) {
		return nil, 0, Error{ProjectionCorrupt}
	}
	digest := hash([]byte(text))
	line, ordinal, skipped := 1, 0, 0
	for start := 0; start < len(text); ordinal++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		end := start + semanticChunkBytes
		if end >= len(text) {
			end = len(text)
		} else {
			for end > start && !utf8.RuneStart(text[end]) {
				end--
			}
		}
		input := text[start:end]
		lastLine := line + strings.Count(input, "\n")
		if input[len(input)-1] == '\n' {
			lastLine--
		}
		r := semanticRecord{Citation: e.Citation, SourceSequence: e.Sequence, ContentDigest: e.Digest, TextDigest: digest, ChunkDigest: hash([]byte(input)), Ordinal: ordinal, TextBytes: len(text), Start: start, End: end, LineStart: line, LineEnd: lastLine}
		if !r.valid(e.Citation.ProjectID, sequence) {
			return nil, 0, Error{ProjectionCorrupt}
		}
		if strings.TrimSpace(input) == "" {
			skipped++
		} else {
			chunks = append(chunks, semanticChunk{r, input})
		}
		line += strings.Count(input, "\n")
		start = end
	}
	return chunks, skipped, nil
}
