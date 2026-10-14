package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"gitcode-mcp/internal/service"
	"math"
	"testing"
)

func TestG5ReviewShapeValidMalformedVectorsMustFallBack(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"zero_norm", "UPDATE repo_doc_vectors SET vector=zeroblob(32)"},
		{"nonfinite", "UPDATE repo_doc_vectors SET vector=X'0000807F00000000000000000000000000000000000000000000000000000000'"},
		{"foreign_dtype", "UPDATE repo_doc_vectors SET dtype='int8'"},
		{"wrong_hash", "UPDATE repo_doc_vectors SET vector_hash='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, path, want := g5ReviewIndexedFixture(t)
			g5ReviewAlterDerived(t, path, tc.sql)
			var got service.LocalProjectSearchResult
			if client.Call(context.Background(), "LocalProjects.Search", service.LocalProjectSearchRequest{Project: "lab", Query: "needle", Mode: "hybrid"}, &got) != nil {
				t.Fatal("invalid derived vector hid current lexical match")
			}
			if got.EffectiveMode != "fulltext" || got.IndexState == "ready" || got.Coverage.State != "lexical-only" || len(got.Hits) != 1 || got.Hits[0].Citation != want.Citation {
				t.Fatal("shape-valid malformed vector was treated as ready semantic coverage")
			}
		})
	}
}

func TestLocalProjectHashConsistentInvalidVectorsStillFallBack(t *testing.T) {
	for _, value := range []float32{0, float32(math.Inf(1))} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			client, path, want := g5ReviewIndexedFixture(t)
			encoded := make([]byte, 32)
			binary.LittleEndian.PutUint32(encoded, math.Float32bits(value))
			sum := sha256.Sum256(encoded)
			g5ReviewAlterDerived(t, path, fmt.Sprintf("UPDATE repo_doc_vectors SET vector=X'%x',vector_hash='%x'", encoded, sum))
			var got service.LocalProjectSearchResult
			if client.Call(context.Background(), "LocalProjects.Search", service.LocalProjectSearchRequest{Project: "lab", Query: "needle", Mode: "hybrid"}, &got) != nil || got.EffectiveMode != "fulltext" || got.IndexState == "ready" || got.Coverage.State != "lexical-only" || len(got.Hits) != 1 || got.Hits[0].Citation != want.Citation {
				t.Fatal("hash-consistent numeric corruption bypassed lexical fallback")
			}
		})
	}
}
