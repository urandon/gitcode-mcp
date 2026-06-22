package index

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestChunkPolicyDeterminismAndMetadata(t *testing.T) {
	body := "# Issue\n\nIssue body links DOC-2.\n\n# Wiki\n\nWiki section.\n\n# Changelog\n\n- one\n- two\n- three\n"
	source := fixtureSource("DOC-1", "wiki", "docs/doc.md", "Doc", "ready", body)
	source.RepoID = "fixture-a"
	source.RecordID = "REC-1"
	source.SnapshotID = "snap-1"
	parsed := ParseSource(source)
	heading := ChunkSourceWithOptions(source, parsed, ChunkOptions{})
	headingAgain := ChunkSourceWithOptions(source, parsed, ChunkOptions{})
	assertChunksIndexedAt(t, heading)
	assertChunksIndexedAt(t, headingAgain)
	if !reflect.DeepEqual(chunksWithoutIndexedAt(heading), chunksWithoutIndexedAt(headingAgain)) {
		t.Fatalf("heading chunks not deterministic")
	}
	sliding := ChunkSourceWithOptions(source, parsed, ChunkOptions{Policy: ChunkPolicySlidingWindow, WindowBytes: 40, OverlapBytes: 10})
	slidingAgain := ChunkSourceWithOptions(source, parsed, ChunkOptions{Policy: ChunkPolicySlidingWindow, WindowBytes: 40, OverlapBytes: 10})
	assertChunksIndexedAt(t, sliding)
	assertChunksIndexedAt(t, slidingAgain)
	if !reflect.DeepEqual(chunksWithoutIndexedAt(sliding), chunksWithoutIndexedAt(slidingAgain)) {
		t.Fatalf("sliding chunks not deterministic")
	}
	ids := map[string]bool{}
	for _, chunks := range [][]Chunk{heading, sliding} {
		for _, chunk := range chunks {
			if ids[chunk.ID] {
				t.Fatalf("chunk id collision: %s", chunk.ID)
			}
			ids[chunk.ID] = true
			if chunk.RepoID != "fixture-a" || chunk.RecordID != "REC-1" || chunk.SnapshotID != "snap-1" || chunk.Policy == "" || chunk.ContentHash == "" || chunk.ByteEnd <= chunk.ByteStart || chunk.LineStart <= 0 || chunk.NormalizedText == "" {
				t.Fatalf("chunk missing metadata: %+v", chunk)
			}
			if chunk.InheritedMetadata["indexed_at"] == "" {
				t.Fatalf("chunk missing indexed_at metadata: %+v", chunk.InheritedMetadata)
			}
		}
	}
}

func TestChunkPolicyBoundaries(t *testing.T) {
	body := "# Big\n\npara one has enough text to exceed the limit.\n\npara two has enough text too.\n```\ncode block should stay together even when it is long\n```\nlast line\n"
	source := fixtureSource("DOC-2", "doc", "docs/big.md", "Big", "ready", body)
	parsed := ParseSource(source)
	heading := ChunkSourceWithOptions(source, parsed, ChunkOptions{MaxBytes: 55})
	if len(heading) < 2 {
		t.Fatalf("heading fallback did not split oversized section: %+v", heading)
	}
	for _, chunk := range heading {
		if strings.Contains(chunk.Text, "```\ncode block") && !strings.Contains(chunk.Text, "long\n```") {
			t.Fatalf("split inside fenced code block: %q", chunk.Text)
		}
	}
	sliding := ChunkSourceWithOptions(source, parsed, ChunkOptions{Policy: ChunkPolicySlidingWindow, WindowBytes: 45, OverlapBytes: 12})
	if len(sliding) < 2 {
		t.Fatalf("sliding window did not split: %+v", sliding)
	}
	for _, chunk := range sliding {
		if chunk.ByteStart > 0 && source.Body[chunk.ByteStart-1] != '\n' {
			t.Fatalf("sliding chunk not line aligned: %+v", chunk)
		}
	}
}

func TestChunkQueryContract(t *testing.T) {
	ctx := context.Background()
	sourceA := fixtureSource("DOC-A", "doc", "docs/a.md", "A", "ready", "# A\nalpha beta\n")
	sourceA.RepoID = "fixture-a"
	sourceA.RecordID = "REC-A"
	sourceB := fixtureSource("DOC-B", "doc", "docs/b.md", "B", "ready", "# B\nbeta gamma\n")
	sourceB.RepoID = "fixture-a"
	chunks := append(ChunkSourceWithOptions(sourceB, ParseSource(sourceB), ChunkOptions{}), ChunkSourceWithOptions(sourceA, ParseSource(sourceA), ChunkOptions{})...)
	chunks = append(chunks, ChunkSourceWithOptions(sourceA, ParseSource(sourceA), ChunkOptions{Policy: ChunkPolicySlidingWindow, WindowBytes: 12, OverlapBytes: 3})...)
	reader := NewMemoryChunkIndex(chunks)
	listed, err := reader.ListChunks(ctx, ChunkQuery{RepoID: "fixture-a", SourceID: "DOC-A", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if listed.Total < 2 || len(listed.Chunks) != 1 || listed.Chunks[0].SourceID != "DOC-A" || listed.Chunks[0].Policy != ChunkPolicyHeading {
		t.Fatalf("list result = %+v", listed)
	}
	paged, err := reader.ListChunks(ctx, ChunkQuery{RepoID: "fixture-a", SourceID: "DOC-A", Offset: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(paged.Chunks) != 1 || paged.Chunks[0].ID == listed.Chunks[0].ID {
		t.Fatalf("pagination unstable: first=%+v second=%+v", listed, paged)
	}
	searched, err := reader.SearchChunks(ctx, ChunkSearchQuery{ChunkQuery: ChunkQuery{RepoID: "fixture-a", Policy: ChunkPolicyHeading}, Query: "gamma"})
	if err != nil {
		t.Fatal(err)
	}
	if searched.Total != 1 || searched.Chunks[0].SourceID != "DOC-B" {
		t.Fatalf("search result = %+v", searched)
	}
	snippet, err := reader.GetSnippet(ctx, SnippetQuery{RepoID: "fixture-a", ChunkID: searched.Chunks[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snippet.Chunks) != 1 || snippet.Chunks[0].SnippetText == "" || snippet.Chunks[0].NormalizedText == "" {
		t.Fatalf("snippet result = %+v", snippet)
	}
}
