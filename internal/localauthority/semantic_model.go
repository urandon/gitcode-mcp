package localauthority

import (
	"encoding/json"
	"math"
	"reflect"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority/embedding"
)

const (
	semanticChunkBytes      = 4096
	semanticTextBytes       = (256 << 10) + 257
	semanticSourceChunks    = 65
	semanticSources         = 4096
	semanticShardRows       = 256
	semanticShardElements   = 262144
	semanticMetadataBytes   = 256 << 10
	semanticShardBytes      = 2 << 20
	semanticShards          = 16
	semanticRows            = 4096
	semanticElements        = 4194304
	semanticGenerationBytes = 32 << 20
	semanticManifestBytes   = 2 << 20
)

// Private format contracts are not runtime capabilities or provider readiness.
// No endpoint, source text, model name or machine path enters these records.
type semanticNamespace struct {
	Schema               int            `json:"schema"`
	ProjectID            authority.UUID `json:"project_id"`
	TrackerInstance      authority.UUID `json:"tracker_instance"`
	ManifestHash         string         `json:"manifest_hash"`
	NetworkHash          string         `json:"network_hash"`
	ModelTagHash         string         `json:"model_tag_hash"`
	ModelDigest          string         `json:"model_digest"`
	Dimensions           int            `json:"dimensions"`
	ProvisioningRevision authority.UUID `json:"provisioning_revision"`
	Normalization        string         `json:"normalization"`
	ChunkPolicy          string         `json:"chunk_policy"`
	InstructionPolicy    string         `json:"instruction_policy"`
	Evidence             string         `json:"evidence"`
}

func newSemanticNamespace(m authority.Manifest, instance authority.UUID, tagHash, digest string, dims int, revision authority.UUID) (semanticNamespace, error) {
	if m.Validate() != nil || m.Network.DataEgress != "loopback_embeddings" {
		return semanticNamespace{}, authority.Error{Code: authority.PolicyDenied}
	}
	p, _ := json.Marshal(m.Network)
	n := semanticNamespace{1, m.ID, instance, manifestHash(m), hash(p), tagHash, digest, dims, revision, "l2_float32_v1", "typed_searchable_utf8_4096_v1", embedding.InstructionPolicy, embedding.TrustedFixedLocalModel}
	if !n.valid() {
		return semanticNamespace{}, Error{ProjectionCorrupt}
	}
	return n, nil
}

func (n semanticNamespace) valid() bool {
	return n.Schema == 1 && n.ProjectID.Valid() && n.TrackerInstance.Valid() && validDigest(n.ManifestHash) && validDigest(n.NetworkHash) && validDigest(n.ModelTagHash) && validDigest(n.ModelDigest) && n.Dimensions >= 1 && n.Dimensions <= 4096 && n.ProvisioningRevision.Valid() && n.Normalization == "l2_float32_v1" && n.ChunkPolicy == "typed_searchable_utf8_4096_v1" && n.InstructionPolicy == embedding.InstructionPolicy && n.Evidence == embedding.TrustedFixedLocalModel
}

func (n semanticNamespace) digest() string { b, _ := json.Marshal(n); return hash(b) }

type semanticRecord struct {
	Citation        authority.Citation         `json:"citation"`
	SourceSequence  uint64                     `json:"source_sequence"`
	ContentDigest   string                     `json:"content_digest"`
	TextDigest      string                     `json:"text_digest"`
	ChunkDigest     string                     `json:"chunk_digest"`
	Ordinal         int                        `json:"ordinal"`
	TextBytes       int                        `json:"text_bytes"`
	Start           int                        `json:"start"`
	End             int                        `json:"end"`
	LineStart       int                        `json:"line_start"`
	LineEnd         int                        `json:"line_end"`
	DocumentLocator *authority.DocumentLocator `json:"document_locator,omitempty"`
}

func (r semanticRecord) valid(project authority.UUID, sequence uint64) bool {
	if r.Citation.Validate() != nil || r.Citation.ProjectID != project || r.SourceSequence < r.Citation.Revision || r.SourceSequence > sequence || sequence > math.MaxInt64 || !validDigest(r.ContentDigest) || !validDigest(r.TextDigest) || !validDigest(r.ChunkDigest) || r.Ordinal < 0 || r.Ordinal >= semanticSourceChunks || r.TextBytes < 1 || r.TextBytes > semanticTextBytes || r.Start < 0 || r.End <= r.Start || r.End > r.TextBytes || r.End-r.Start > semanticChunkBytes || r.LineStart < 1 || r.LineEnd < r.LineStart || r.LineEnd > r.TextBytes+1 {
		return false
	}
	// Every non-final UTF8-boundary chunk consumes 4093..4096 bytes.
	// These are necessary format bounds, not a substitute for rehydration.
	if r.Ordinal == 0 && (r.Start != 0 || r.LineStart != 1) || r.Ordinal > 0 && (r.Start < r.Ordinal*(semanticChunkBytes-3) || r.Start > r.Ordinal*semanticChunkBytes || r.TextBytes <= semanticChunkBytes+(r.Ordinal-1)*(semanticChunkBytes-3)) || r.LineStart > r.Start+1 || r.LineEnd > r.LineStart+r.End-r.Start-1 {
		return false
	}
	if r.TextBytes-r.Start <= semanticChunkBytes {
		if r.End != r.TextBytes {
			return false
		}
	} else if r.End-r.Start < semanticChunkBytes-3 {
		return false
	}
	if d := r.DocumentLocator; d != nil && (r.Citation.Kind != authority.Document || d.Citation != r.Citation || d.Validate() != nil || len(d.CommitOID) != 64 || len(d.BlobOID) != 64) {
		return false
	}
	return true
}

// The same source must retain one captured revision/proof across all shards.
func semanticOrdered(a, b semanticRecord) bool {
	if a.Citation.EntityID != b.Citation.EntityID {
		return a.Citation.EntityID < b.Citation.EntityID
	}
	return a.Citation == b.Citation && a.SourceSequence == b.SourceSequence && a.ContentDigest == b.ContentDigest && a.TextDigest == b.TextDigest && a.TextBytes == b.TextBytes && reflect.DeepEqual(a.DocumentLocator, b.DocumentLocator) && a.Ordinal < b.Ordinal && a.End <= b.Start && a.LineEnd <= b.LineStart && b.LineStart <= a.LineEnd+(b.Start-a.End)+1
}

type semanticSelection struct {
	Sources    int    `json:"sources"`
	Eligible   int    `json:"eligible"`
	Whitespace int    `json:"whitespace"`
	Stored     int    `json:"stored"`
	Omitted    int    `json:"omitted"`
	Exhausted  bool   `json:"exhausted"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
}

func (s semanticSelection) valid() bool {
	if s.Sources < 0 || s.Sources > semanticSources || s.Eligible < 0 || s.Whitespace < 0 || s.Eligible > s.Sources*semanticSourceChunks || s.Whitespace > s.Sources*semanticSourceChunks-s.Eligible || s.Stored < 0 || s.Stored > semanticRows || s.Omitted < 0 || s.Eligible != s.Stored+s.Omitted {
		return false
	}
	if s.Exhausted && s.Omitted == 0 {
		return s.State == "complete" && s.Reason == "none"
	}
	if s.State != "bounded_partial" {
		return false
	}
	switch s.Reason {
	case "source_limit":
		return !s.Exhausted && s.Sources == semanticSources
	case "row_limit":
		return s.Stored == semanticRows
	case "element_limit", "byte_limit", "inspection_turn_limit":
		return true
	}
	return false
}

type semanticShardDescriptor struct {
	Ordinal  int    `json:"ordinal"`
	Digest   string `json:"digest"`
	Bytes    int    `json:"bytes"`
	Rows     int    `json:"rows"`
	Elements int    `json:"elements"`
}

type semanticGeneration struct {
	Schema              int                       `json:"schema"`
	Namespace           semanticNamespace         `json:"namespace"`
	Epoch               authority.UUID            `json:"epoch"`
	BuildID             authority.UUID            `json:"build_id"`
	Generation          uint64                    `json:"generation"`
	SelectionSequence   uint64                    `json:"selection_sequence"`
	RevalidatedSequence uint64                    `json:"revalidated_sequence"`
	Selection           semanticSelection         `json:"selection"`
	Shards              []semanticShardDescriptor `json:"shards"`
	ShardBytes          int                       `json:"shard_bytes"`
	Elements            int                       `json:"elements"`
}

func (g semanticGeneration) valid() bool {
	if g.Schema != 1 || !g.Namespace.valid() || !g.Epoch.Valid() || !g.BuildID.Valid() || g.Generation < 1 || g.Generation > math.MaxInt64 || g.SelectionSequence > g.RevalidatedSequence || g.RevalidatedSequence > math.MaxInt64 || !g.Selection.valid() || g.Shards == nil || len(g.Shards) > semanticShards || g.ShardBytes < 0 || g.ShardBytes > semanticGenerationBytes || g.Elements < 0 || g.Elements > semanticElements || uint64(g.Selection.Sources) > g.SelectionSequence {
		return false
	}
	rows, elems, size := 0, 0, 0
	for i, d := range g.Shards {
		if d.Ordinal != i || !validDigest(d.Digest) || d.Rows < 1 || d.Rows > semanticShardRows || d.Elements != d.Rows*g.Namespace.Dimensions || d.Elements > semanticShardElements || d.Bytes < 72+4*d.Elements || d.Bytes > semanticShardBytes {
			return false
		}
		rows += d.Rows
		elems += d.Elements
		size += d.Bytes
	}
	return rows == g.Selection.Stored && elems == g.Elements && size == g.ShardBytes
}

type semanticHead struct {
	Schema          int            `json:"schema"`
	NamespaceDigest string         `json:"namespace_digest"`
	Epoch           authority.UUID `json:"epoch"`
	BuildID         authority.UUID `json:"build_id"`
	Generation      uint64         `json:"generation"`
	ManifestDigest  string         `json:"manifest_digest"`
}

func (h semanticHead) valid() bool {
	return h.Schema == 1 && validDigest(h.NamespaceDigest) && h.Epoch.Valid() && h.BuildID.Valid() && h.Generation > 0 && h.Generation <= math.MaxInt64 && validDigest(h.ManifestDigest)
}
