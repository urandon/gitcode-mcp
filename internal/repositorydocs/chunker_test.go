package repositorydocs

import (
	"strings"
	"testing"
)

func TestChunkDocumentDeterministicBoundedAndMultilingual(t *testing.T) {
	data := []byte("# English\n\nРусский текст и 中文文档.\n\n" + strings.Repeat("bounded content\n", 20))
	first, err := ChunkDocument("blob-a", data, 80)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ChunkDocument("blob-a", data, 80)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 2 || len(first) != len(second) {
		t.Fatalf("chunk counts = %d and %d", len(first), len(second))
	}
	for idx := range first {
		if first[idx].ID != second[idx].ID || first[idx].ByteEnd-first[idx].ByteStart > 80 || first[idx].Text == "" {
			t.Fatalf("chunk[%d] = %#v", idx, first[idx])
		}
	}
}

func TestChunkDocumentRejectsBinary(t *testing.T) {
	if _, err := ChunkDocument("blob", []byte{0xff, 0xfe}, 64); err == nil {
		t.Fatal("binary input was accepted")
	}
}

func BenchmarkRepositoryDocsChunking(b *testing.B) {
	data := []byte(strings.Repeat("# Section\n\nEnglish Русский 中文 deterministic repository documentation.\n\n", 4096))
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for index := 0; index < b.N; index++ {
		chunks, err := ChunkDocument("benchmark-blob", data, DefaultChunkBytes)
		if err != nil || len(chunks) == 0 {
			b.Fatalf("chunks=%d err=%v", len(chunks), err)
		}
	}
}
