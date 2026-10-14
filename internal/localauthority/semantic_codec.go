package localauthority

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"strings"

	"gitcode-mcp/internal/authority"
)

type semanticShard struct {
	records []semanticRecord
	vectors [][]float32
}
type semanticShardMetadata struct {
	Schema  int              `json:"schema"`
	Records []semanticRecord `json:"records"`
}

// In addition to the authority closed/duplicate/depth/required-field grammar,
// no null value is legal here, even for an optional document locator.
func decodeSemanticJSON(b []byte, target any, cap int) error {
	if len(b) > cap || authority.DecodeStoredJSON(b, target, cap) != nil {
		return Error{ProjectionCorrupt}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	for {
		t, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil || t == nil {
			return Error{ProjectionCorrupt}
		}
	}
}

func semanticVectorValid(v []float32, dims int, canonical bool) bool {
	if len(v) != dims {
		return false
	}
	sum := float64(0)
	for _, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) || canonical && math.Float32bits(f) == 0x80000000 {
			return false
		}
		sum += float64(f) * float64(f)
	}
	return math.Abs(sum-1) <= 1e-4
}

func semanticShardValid(s semanticShard, n semanticNamespace, sequence uint64) bool {
	if !n.valid() || len(s.records) < 1 || len(s.records) > semanticShardRows || len(s.records) != len(s.vectors) || len(s.records)*n.Dimensions > semanticShardElements {
		return false
	}
	for i, r := range s.records {
		if !r.valid(n.ProjectID, sequence) || !semanticVectorValid(s.vectors[i], n.Dimensions, false) || i > 0 && !semanticOrdered(s.records[i-1], r) {
			return false
		}
	}
	return true
}

func semanticHeaderIdentity(n semanticNamespace, build authority.UUID) ([]byte, []byte, bool) {
	if !n.valid() || !build.Valid() {
		return nil, nil, false
	}
	a, _ := hex.DecodeString(n.digest())
	b, _ := hex.DecodeString(strings.ReplaceAll(string(build), "-", ""))
	return a, b, true
}

func encodeSemanticShard(s semanticShard, n semanticNamespace, build authority.UUID, ordinal int, sequence uint64) ([]byte, error) {
	ns, id, ok := semanticHeaderIdentity(n, build)
	if !ok || ordinal < 0 || ordinal >= semanticShards || !semanticShardValid(s, n, sequence) {
		return nil, Error{ProjectionCorrupt}
	}
	meta, err := json.Marshal(semanticShardMetadata{1, s.records})
	elements := len(s.records) * n.Dimensions
	size := 72 + len(meta) + 4*elements
	if err != nil || len(meta) > semanticMetadataBytes || size > semanticShardBytes {
		return nil, Error{ProjectionCorrupt}
	}
	b := make([]byte, size)
	copy(b, "GCSVEC01")
	binary.LittleEndian.PutUint16(b[8:10], 1)
	b[10] = byte(ordinal)
	binary.LittleEndian.PutUint16(b[12:14], uint16(n.Dimensions))
	binary.LittleEndian.PutUint16(b[14:16], uint16(len(s.records)))
	binary.LittleEndian.PutUint32(b[16:20], uint32(len(meta)))
	binary.LittleEndian.PutUint32(b[20:24], uint32(elements))
	copy(b[24:56], ns)
	copy(b[56:72], id)
	copy(b[72:], meta)
	offset := 72 + len(meta)
	for _, v := range s.vectors {
		for _, f := range v {
			if f == 0 {
				f = 0
			}
			binary.LittleEndian.PutUint32(b[offset:offset+4], math.Float32bits(f))
			offset += 4
		}
	}
	return b, nil
}

func decodeSemanticShard(b []byte, n semanticNamespace, build authority.UUID, ordinal int, sequence uint64) (semanticShard, error) {
	bad := Error{ProjectionCorrupt}
	zero := semanticShard{}
	ns, id, ok := semanticHeaderIdentity(n, build)
	if !ok || ordinal < 0 || ordinal >= semanticShards || len(b) < 72 || len(b) > semanticShardBytes || string(b[:8]) != "GCSVEC01" || binary.LittleEndian.Uint16(b[8:10]) != 1 || b[10] != byte(ordinal) || b[11] != 0 || int(binary.LittleEndian.Uint16(b[12:14])) != n.Dimensions || !bytes.Equal(b[24:56], ns) || !bytes.Equal(b[56:72], id) {
		return zero, bad
	}
	rows := uint64(binary.LittleEndian.Uint16(b[14:16]))
	metaLen := uint64(binary.LittleEndian.Uint32(b[16:20]))
	elems := uint64(binary.LittleEndian.Uint32(b[20:24]))
	// All arithmetic is wide and bounded before allocating any DTO/vector.
	if rows < 1 || rows > semanticShardRows || metaLen > semanticMetadataBytes || elems > semanticShardElements || elems != rows*uint64(n.Dimensions) || 72+metaLen+4*elems != uint64(len(b)) {
		return zero, bad
	}
	// Preflight the array cardinality before decoding an attacker-sized slice.
	var raw struct {
		Schema  int             `json:"schema"`
		Records json.RawMessage `json:"records"`
	}
	if decodeSemanticJSON(b[72:72+metaLen], &raw, semanticMetadataBytes) != nil || raw.Schema != 1 {
		return zero, bad
	}
	d := json.NewDecoder(bytes.NewReader(raw.Records))
	t, err := d.Token()
	if err != nil || t != json.Delim('[') {
		return zero, bad
	}
	s := semanticShard{records: make([]semanticRecord, 0, int(rows)), vectors: make([][]float32, 0, int(rows))}
	for d.More() {
		if len(s.records) >= int(rows) {
			return zero, bad
		}
		var body json.RawMessage
		if d.Decode(&body) != nil || len(body) > 2048 {
			return zero, bad
		}
		var r semanticRecord
		if decodeSemanticJSON(body, &r, 2048) != nil {
			return zero, bad
		}
		s.records = append(s.records, r)
	}
	if _, err = d.Token(); err != nil || len(s.records) != int(rows) {
		return zero, bad
	}
	if _, err = d.Token(); err != io.EOF {
		return zero, bad
	}
	offset := int(72 + metaLen)
	for range s.records {
		v := make([]float32, n.Dimensions)
		for i := range v {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[offset : offset+4]))
			offset += 4
		}
		if !semanticVectorValid(v, n.Dimensions, true) {
			return zero, bad
		}
		s.vectors = append(s.vectors, v)
	}
	if !semanticShardValid(s, n, sequence) {
		return zero, bad
	}
	return s, nil
}

func encodeSemanticGeneration(g semanticGeneration) ([]byte, error) {
	if !g.valid() {
		return nil, Error{ProjectionCorrupt}
	}
	b, err := json.Marshal(g)
	if err != nil || len(b) > semanticManifestBytes || len(b)+g.ShardBytes > semanticGenerationBytes {
		return nil, Error{ProjectionCorrupt}
	}
	return b, nil
}

// Independent binding and actual shard bytes are mandatory; metadata claims
// alone cannot confer publication, coverage, readiness or canonical provenance.
func decodeSemanticGeneration(b []byte, n semanticNamespace, build authority.UUID, shards [][]byte) (semanticGeneration, error) {
	bad := Error{ProjectionCorrupt}
	zero := semanticGeneration{}
	if !n.valid() || !build.Valid() || len(shards) > semanticShards {
		return zero, bad
	}
	var g semanticGeneration
	if decodeSemanticJSON(b, &g, semanticManifestBytes) != nil || !g.valid() || g.Namespace != n || g.BuildID != build || len(g.Shards) != len(shards) || len(b)+g.ShardBytes > semanticGenerationBytes {
		return zero, bad
	}
	var last *semanticRecord
	distinct := 0
	inspectedChunks, sourceChunks := 0, 0
	for i, raw := range shards {
		d := g.Shards[i]
		if len(raw) != d.Bytes || hash(raw) != d.Digest {
			return zero, bad
		}
		s, err := decodeSemanticShard(raw, n, build, i, g.SelectionSequence)
		if err != nil || len(s.records) != d.Rows || len(s.records)*n.Dimensions != d.Elements {
			return zero, bad
		}
		if last != nil && !semanticOrdered(*last, s.records[0]) {
			return zero, bad
		}
		for _, r := range s.records {
			if last == nil || last.Citation.EntityID != r.Citation.EntityID {
				distinct++
				sourceChunks = 0
			}
			if distinct > g.Selection.Sources {
				return zero, bad
			}
			implied := r.Ordinal + 1
			if g.Selection.State == "complete" {
				whole := (r.TextBytes + semanticChunkBytes - 1) / semanticChunkBytes
				if whole > implied {
					implied = whole
				}
			}
			if implied > sourceChunks {
				inspectedChunks += implied - sourceChunks
				sourceChunks = implied
			}
			if inspectedChunks > g.Selection.Eligible+g.Selection.Whitespace {
				return zero, bad
			}
			captured := r
			last = &captured
		}
	}
	return g, nil
}

func encodeSemanticHead(h semanticHead) ([]byte, error) {
	if !h.valid() {
		return nil, Error{ProjectionCorrupt}
	}
	b, err := json.Marshal(h)
	if err != nil || len(b) > 32<<10 {
		return nil, Error{ProjectionCorrupt}
	}
	return b, nil
}

func decodeSemanticHead(b []byte, g semanticGeneration, manifest []byte) (semanticHead, error) {
	var h semanticHead
	if decodeSemanticJSON(b, &h, 32<<10) != nil || !h.valid() || !g.valid() || h.NamespaceDigest != g.Namespace.digest() || h.Epoch != g.Epoch || h.BuildID != g.BuildID || h.Generation != g.Generation || h.ManifestDigest != hash(manifest) {
		return semanticHead{}, Error{ProjectionCorrupt}
	}
	// Revalidate the supplied bytes against the independent decoded generation.
	var actual semanticGeneration
	if decodeSemanticJSON(manifest, &actual, semanticManifestBytes) != nil || !actual.valid() || !reflectSemanticGenerationEqual(actual, g) || len(manifest)+g.ShardBytes > semanticGenerationBytes {
		return semanticHead{}, Error{ProjectionCorrupt}
	}
	return h, nil
}

func reflectSemanticGenerationEqual(a, b semanticGeneration) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
