package localauthority

import (
	"gitcode-mcp/internal/authority"
	"testing"
)

func TestReviewSemanticSourcesMustCoverDistinctRows(t *testing.T) {
	n, s, seq := semanticFixtureShard(t)
	first := s.records[0]
	s.records = nil
	s.vectors = nil
	for _, entity := range []authority.UUID{
		"22222222-2222-4222-8222-222222222221",
		"22222222-2222-4222-8222-222222222222",
		"22222222-2222-4222-8222-222222222223",
	} {
		r := first
		r.Citation.EntityID = entity
		s.records = append(s.records, r)
		s.vectors = append(s.vectors, []float32{0, 1})
	}
	b, err := encodeSemanticShard(s, n, semanticTestBuild, 0, seq)
	if err != nil {
		t.Fatal("negative shard fixture unavailable")
	}
	g := semanticFixtureGeneration(n, semanticTestBuild, seq, b)
	if g.Selection.Sources != 1 || g.Selection.Stored != 3 {
		t.Fatal("negative count fixture changed")
	}
	m, err := encodeSemanticGeneration(g)
	if err != nil {
		return
	}
	if _, err = decodeSemanticGeneration(m, n, semanticTestBuild, [][]byte{b}); err == nil {
		t.Fatal("one inspected source claims stored vectors from three distinct sources")
	}
}

func TestReviewSemanticChunkOrdinalRequiresPlausibleSpan(t *testing.T) {
	n, s, seq := semanticFixtureShard(t)
	s.records[0].Ordinal = 1
	if s.records[0].Start != 0 || s.records[0].TextBytes > semanticChunkBytes {
		t.Fatal("negative span fixture changed")
	}
	b, err := encodeSemanticShard(s, n, semanticTestBuild, 0, seq)
	if err != nil {
		return
	}
	if _, err = decodeSemanticShard(b, n, semanticTestBuild, 0, seq); err == nil {
		t.Fatal("second deterministic chunk begins at byte zero in one-chunk text")
	}
}
