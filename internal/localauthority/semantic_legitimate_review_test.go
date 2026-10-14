package localauthority

import (
	"context"
	"gitcode-mcp/internal/authority"
	"strings"
	"testing"
)

func TestReviewSemanticLegitimateMultiChunkSelections(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Mutate(context.Background(), request(t, "issue.create", "semantic-legitimate-review", authority.IssuePayload{Title: "Synthetic", Body: strings.Repeat("x", 5000), State: "open"}, authority.MutationReceipt{}), testActor)
	if err != nil {
		t.Fatal("canonical source fixture unavailable")
	}
	p := semanticSnapshot(t, s, authority.Issue)
	chunks, skipped := semanticCapture(t, p)
	if len(chunks) != 2 || skipped != 0 {
		t.Fatal("canonical multi-chunk fixture changed")
	}
	n := semanticFixtureNamespace(t)
	for _, mode := range []string{"complete_both", "partial_prefix", "partial_final", "false_complete_prefix", "false_complete_final"} {
		t.Run(mode, func(t *testing.T) {
			selected := chunks
			if strings.HasSuffix(mode, "prefix") {
				selected = chunks[:1]
			}
			if strings.HasSuffix(mode, "final") {
				selected = chunks[1:]
			}
			shard := semanticShard{}
			for _, c := range selected {
				shard.records = append(shard.records, c.record)
				shard.vectors = append(shard.vectors, []float32{0, 1})
			}
			b, err := encodeSemanticShard(shard, n, semanticTestBuild, 0, p.Sequence)
			if err != nil {
				t.Fatal("legitimate shard fixture unavailable")
			}
			g := semanticFixtureGeneration(n, semanticTestBuild, p.Sequence, b)
			if strings.HasPrefix(mode, "partial_") {
				g.Selection.Exhausted = false
				g.Selection.State = "bounded_partial"
				g.Selection.Reason = "inspection_turn_limit"
				g.Selection.Eligible = 2
				g.Selection.Omitted = 1
			}
			m, err := encodeSemanticGeneration(g)
			if err != nil {
				t.Fatal("manifest fixture unavailable")
			}
			_, err = decodeSemanticGeneration(m, n, semanticTestBuild, [][]byte{b})
			if strings.HasPrefix(mode, "false_") {
				if err == nil {
					t.Fatal("false complete source prefix/suffix accepted")
				}
			} else if err != nil {
				t.Fatal("legitimate multi-chunk selection rejected")
			}
		})
	}
}

func TestReviewSemanticLegitimateWhitespaceAccounting(t *testing.T) {
	s, _, _ := testStore(t)
	_, err := s.Mutate(context.Background(), request(t, "issue.create", "semantic-whitespace-review", authority.IssuePayload{Title: "Synthetic", Body: strings.Repeat(" ", 8192) + "end", State: "open"}, authority.MutationReceipt{}), testActor)
	if err != nil {
		t.Fatal("canonical whitespace fixture unavailable")
	}
	p := semanticSnapshot(t, s, authority.Issue)
	chunks, skipped := semanticCapture(t, p)
	if len(chunks) != 2 || skipped != 1 {
		t.Fatal("canonical whitespace fixture changed")
	}
	n := semanticFixtureNamespace(t)
	shard := semanticShard{}
	for _, c := range chunks {
		shard.records = append(shard.records, c.record)
		shard.vectors = append(shard.vectors, []float32{0, 1})
	}
	b, err := encodeSemanticShard(shard, n, semanticTestBuild, 0, p.Sequence)
	if err != nil {
		t.Fatal("whitespace shard fixture unavailable")
	}
	g := semanticFixtureGeneration(n, semanticTestBuild, p.Sequence, b)
	g.Selection.Whitespace = skipped
	m, err := encodeSemanticGeneration(g)
	if err != nil {
		t.Fatal("whitespace manifest fixture unavailable")
	}
	if _, err = decodeSemanticGeneration(m, n, semanticTestBuild, [][]byte{b}); err != nil {
		t.Fatal("legitimate skipped whitespace accounting rejected")
	}
}
