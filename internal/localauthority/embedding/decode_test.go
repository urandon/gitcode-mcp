package embedding

import (
	"fmt"
	"strings"
	"testing"
)

func TestEmbeddingGrammarBoundsAndFiniteVectors(t *testing.T) {
	valid := fixtureVectors(1)
	vectorCase := func(vectors string) string { return strings.Replace(valid, `[[3,4]]`, vectors, 1) }
	cases := []string{
		vectorCase(`[]`), vectorCase(`[[1]]`), vectorCase(`[[1,2,3]]`), vectorCase(`[[1,2],[3,4]]`), vectorCase(`[[0,0]]`),
		vectorCase(`[[1e100,2]]`), vectorCase(`[[1e400,2]]`), vectorCase(`[[1e-400,0]]`), vectorCase(`[[null,2]]`), vectorCase(`[["1",2]]`),
		vectorCase(`[[{"value":1},2]]`), vectorCase(`null`),
		strings.TrimSuffix(valid, "}") + `,"extra":"synthetic"}`,
		strings.Replace(valid, `"model":`, `"model":"example-embed:small","model":`, 1),
		strings.Replace(valid, `"prompt_eval_count":2`, `"prompt_eval_count":-1`, 1), valid + ` {}`,
		strings.Replace(valid, `"load_duration":0`, `"load_duration":null`, 1),
		strings.Replace(valid, `"total_duration":1`, `"total_duration":18446744073709551615`, 1),
		strings.Replace(valid, `"total_duration":1`, `"total_duration":-1`, 1),
		vectorCase(`[[` + strings.Repeat("1,", 4096) + `1]]`),
	}
	for i, body := range cases {
		vectors, err := decodeEmbedding([]byte(body), fixtureModel, 1, 2)
		if err == nil || vectors != nil {
			t.Fatalf("invalid vector case %d accepted", i)
		}
		requireCode(t, err, InvalidResponse)
	}
	_, err := decodeEmbedding([]byte(strings.Replace(fixtureVectors(1), fixtureModel, "different:small", 1)), fixtureModel, 1, 2)
	requireCode(t, err, ModelMismatch)
	_, err = decodeEmbedding([]byte(fixtureVectors(1)), fixtureModel, 9, 2)
	requireCode(t, err, InvalidResponse)
	_, err = decodeEmbedding([]byte(fixtureVectors(1)), fixtureModel, 1, 4097)
	requireCode(t, err, InvalidResponse)
}

func TestInventoryClosedBoundedAndQualified(t *testing.T) {
	valid := fixtureInventory(fixtureDigest)
	cases := []string{
		`{"models":null}`, `{"models":[]}`, `{"models":[],"unknown":true}`,
		strings.Replace(valid, `"format":"gguf"`, `"format":"cloud"`, 1),
		strings.Replace(valid, `"digest":"`+fixtureDigest+`"`, `"digest":"bad"`, 1),
		strings.Replace(valid, `"size":1234`, `"size":-1`, 1),
		strings.Replace(valid, `"families":["example"]`, `"families":[`+strings.Repeat(`"example",`, 16)+`"example"]`, 1),
		strings.Replace(valid, `"details":{`, `"details":{"private_field":true,`, 1),
		strings.Replace(valid, `"model":"`+fixtureModel+`"`, `"model":"different:small"`, 1),
		strings.Replace(valid, `"name":"`+fixtureModel+`"`, `"name":"`+strings.Repeat("x", 129)+`"`, 1),
	}
	for i, body := range cases {
		if err := decodeInventory([]byte(body), fixtureModel, fixtureDigest); err == nil {
			t.Fatalf("invalid inventory case %d accepted", i)
		}
	}
	entry := strings.TrimSuffix(strings.TrimPrefix(valid, `{"models":[`), `]}`)
	if err := decodeInventory([]byte(`{"models":[`+entry+`,`+entry+`]}`), fixtureModel, fixtureDigest); err == nil {
		t.Fatal("duplicate inventory model accepted")
	}
	entries := make([]string, 129)
	for i := range entries {
		entries[i] = strings.Replace(entry, `"name":"`+fixtureModel+`"`, `"name":"`+fmt.Sprintf("fixture-%03d", i)+`"`, 1)
	}
	requireCode(t, decodeInventory([]byte(`{"models":[`+strings.Join(entries, ",")+`]}`), fixtureModel, fixtureDigest), InvalidResponse)
	if err := decodeInventory([]byte(valid), fixtureModel, fixtureDigest); err != nil {
		t.Fatal("qualified inventory rejected")
	}
}
