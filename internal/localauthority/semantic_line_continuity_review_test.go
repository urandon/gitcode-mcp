package localauthority

import (
	"context"
	"gitcode-mcp/internal/authority"
	"strings"
	"testing"
)

func TestReviewSemanticAdjacentChunksConserveLineContinuity(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Mutate(context.Background(), request(t, "issue.create", "semantic-lines-review", authority.IssuePayload{Title: "Synthetic", Body: strings.Repeat("x", 5000), State: "open"}, authority.MutationReceipt{}), testActor)
	if err != nil {
		t.Fatal("canonical source fixture unavailable")
	}
	p := semanticSnapshot(t, s, authority.Issue)
	chunks, skipped := semanticCapture(t, p)
	if len(chunks) != 2 || skipped != 0 || chunks[0].record.End != chunks[1].record.Start {
		t.Fatal("adjacent canonical chunk fixture changed")
	}
	second := chunks[1].record
	second.LineStart = 100
	second.LineEnd = 100
	n := semanticFixtureNamespace(t)
	b, err := encodeSemanticShard(semanticShard{[]semanticRecord{chunks[0].record, second}, [][]float32{{0, 1}, {0, 1}}}, n, semanticTestBuild, 0, p.Sequence)
	if err != nil {
		return
	}
	if _, err = decodeSemanticShard(b, n, semanticTestBuild, 0, p.Sequence); err == nil {
		t.Fatal("adjacent chunk spans jump many lines without any intervening bytes")
	}
}
