package localauthority

import (
	"context"
	"gitcode-mcp/internal/authority"
	"strings"
	"testing"
)

func TestReviewSemanticCompleteConservesImpliedChunks(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Mutate(context.Background(), request(t, "issue.create", "semantic-count-review", authority.IssuePayload{Title: "Synthetic", Body: strings.Repeat("x", 5000), State: "open"}, authority.MutationReceipt{}), testActor)
	if err != nil {
		t.Fatal("canonical source fixture unavailable")
	}
	p := semanticSnapshot(t, s, authority.Issue)
	chunks, skipped := semanticCapture(t, p)
	if len(chunks) != 2 || skipped != 0 || chunks[1].record.Ordinal != 1 {
		t.Fatal("canonical multi-chunk fixture changed")
	}
	n := semanticFixtureNamespace(t)
	b, err := encodeSemanticShard(semanticShard{[]semanticRecord{chunks[1].record}, [][]float32{{0, 1}}}, n, semanticTestBuild, 0, p.Sequence)
	if err != nil {
		t.Fatal("selected final chunk fixture unavailable")
	}
	g := semanticFixtureGeneration(n, semanticTestBuild, p.Sequence, b)
	m, err := encodeSemanticGeneration(g)
	if err != nil {
		return
	}
	if _, err = decodeSemanticGeneration(m, n, semanticTestBuild, [][]byte{b}); err == nil {
		t.Fatal("complete generation omits a required prior chunk without whitespace or omission accounting")
	}
}

func TestReviewSemanticLineSpanHasPossibleByteCount(t *testing.T) {
	n, s, seq := semanticFixtureShard(t)
	r := s.records[0]
	r.LineEnd = r.LineStart + r.End - r.Start
	s.records[0] = r
	b, err := encodeSemanticShard(s, n, semanticTestBuild, 0, seq)
	if err != nil {
		return
	}
	if _, err = decodeSemanticShard(b, n, semanticTestBuild, 0, seq); err == nil {
		t.Fatal("line span exceeds maximum possible newline count for its bytes")
	}
}
