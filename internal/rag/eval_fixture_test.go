package rag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type evalFixture struct {
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Profile          string     `json:"profile"`
	ChunkPolicyID    string     `json:"chunk_policy_id"`
	LanguagePolicyID string     `json:"language_policy_id"`
	Cases            []evalCase `json:"cases"`
}

type evalCase struct {
	ID              string      `json:"id"`
	Language        string      `json:"language"`
	Query           string      `json:"query"`
	QueryVector     []float32   `json:"query_vector"`
	ExpectedChunkID string      `json:"expected_chunk_id"`
	Chunks          []evalChunk `json:"chunks"`
}

type evalChunk struct {
	ID       string    `json:"id"`
	SourceID string    `json:"source_id"`
	Text     string    `json:"text"`
	Vector   []float32 `json:"vector"`
}

func loadMultilingualEvalFixture(t *testing.T) evalFixture {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "rag", "eval", "multilingual_cases.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read eval fixture: %v", err)
	}
	var fixture evalFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode eval fixture: %v", err)
	}
	if fixture.Name == "" || fixture.Profile == "" || len(fixture.Cases) == 0 {
		t.Fatalf("eval fixture missing required fields: %#v", fixture)
	}
	for _, tc := range fixture.Cases {
		if tc.ID == "" || tc.Language == "" || tc.Query == "" || tc.ExpectedChunkID == "" || len(tc.QueryVector) == 0 || len(tc.Chunks) == 0 {
			t.Fatalf("eval case missing required fields: %#v", tc)
		}
	}
	return fixture
}
