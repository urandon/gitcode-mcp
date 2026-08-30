package repositorydocs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	DefaultChunkBytes = 4096
)

// DefaultChunkPolicyID identifies the complete default processing policy, not
// just the chunker family. Custom chunk sizes and guards receive distinct IDs.
var DefaultChunkPolicyID = DefaultProcessingPolicy().ID()

type Chunk struct {
	ID                   string
	ByteStart            int
	ByteEnd              int
	LineStart            int
	LineEnd              int
	RawSliceDigest       string
	EmbeddingInputDigest string
	Text                 string
}

// ChunkDocument creates deterministic, bounded chunks. Source text lives only
// in the returned in-memory value and must be discarded after provider use.
func ChunkDocument(objectIdentity string, data []byte, maxBytes int) ([]Chunk, error) {
	return ChunkDocumentWithPolicy(objectIdentity, data, ProcessingPolicyFor(0, maxBytes))
}

// ChunkDocumentWithPolicy creates chunks whose IDs name the exact processing
// policy that produced them.
func ChunkDocumentWithPolicy(objectIdentity string, data []byte, policy ProcessingPolicy) ([]Chunk, error) {
	policy = policy.normalized()
	maxBytes := policy.ChunkBytes
	policyID := policy.ID()
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("repository docs: document is not valid UTF-8")
	}
	if len(data) == 0 {
		return nil, nil
	}
	var chunks []Chunk
	start := 0
	for start < len(data) {
		end := start + maxBytes
		if end >= len(data) {
			end = len(data)
		} else {
			for end > start && !utf8.RuneStart(data[end]) {
				end--
			}
			window := data[start:end]
			if boundary := bytes.LastIndex(window, []byte("\n\n")); boundary >= maxBytes/3 {
				end = start + boundary + 2
			} else if boundary := bytes.LastIndexByte(window, '\n'); boundary >= maxBytes/3 {
				end = start + boundary + 1
			}
		}
		if end <= start {
			return nil, fmt.Errorf("repository docs: chunker made no progress")
		}
		raw := data[start:end]
		normalized := normalizeEmbeddingInput(string(raw))
		if normalized != "" {
			rawDigest := digestBytes(raw)
			inputDigest := digestBytes([]byte(normalized))
			lineStart := 1 + bytes.Count(data[:start], []byte{'\n'})
			lineEnd := lineStart + bytes.Count(raw, []byte{'\n'})
			if len(raw) > 0 && raw[len(raw)-1] == '\n' && lineEnd > lineStart {
				lineEnd--
			}
			idSum := sha256.Sum256([]byte(strings.Join([]string{objectIdentity, fmt.Sprint(start), fmt.Sprint(end), rawDigest, inputDigest, policyID}, "\x00")))
			chunks = append(chunks, Chunk{ID: "repo-doc-chunk-" + hex.EncodeToString(idSum[:]), ByteStart: start, ByteEnd: end, LineStart: lineStart, LineEnd: lineEnd, RawSliceDigest: rawDigest, EmbeddingInputDigest: inputDigest, Text: normalized})
		}
		start = end
	}
	return chunks, nil
}

func normalizeEmbeddingInput(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for idx := range lines {
		lines[idx] = strings.TrimRight(lines[idx], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
