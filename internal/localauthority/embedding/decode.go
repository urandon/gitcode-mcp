package embedding

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strings"

	"gitcode-mcp/internal/authority"
)

type inventoryModel struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	ModifiedAt string `json:"modified_at"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
	Details    struct {
		ParentModel       string   `json:"parent_model"`
		Format            string   `json:"format"`
		Family            string   `json:"family"`
		Families          []string `json:"families"`
		ParameterSize     string   `json:"parameter_size"`
		QuantizationLevel string   `json:"quantization_level"`
	} `json:"details"`
}

func decodeInventory(body []byte, model, digest string) error {
	// Keep the outer grammar closed, then decode one model at a time; an
	// inventory with unbounded entries never allocates an unbounded model slice.
	var outer struct {
		Models json.RawMessage `json:"models"`
	}
	if authority.DecodeJSON(body, &outer) != nil {
		return Error{InvalidResponse}
	}
	d := json.NewDecoder(bytes.NewReader(outer.Models))
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return Error{InvalidResponse}
	}
	seen := make(map[string]bool)
	found := false
	for d.More() {
		if len(seen) == 128 {
			return Error{InvalidResponse}
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return Error{InvalidResponse}
		}
		var entry inventoryModel
		if authority.DecodeJSON(raw, &entry) != nil || entry.Name == "" || len(entry.Name) > 128 || len(entry.Model) > 128 || seen[entry.Name] || entry.Size < 0 || !digestPattern.MatchString(entry.Digest) ||
			len(entry.ModifiedAt) > 128 || len(entry.Details.ParentModel) > 128 || len(entry.Details.Family) > 128 || len(entry.Details.Families) > 16 || len(entry.Details.ParameterSize) > 128 || len(entry.Details.QuantizationLevel) > 128 {
			return Error{InvalidResponse}
		}
		for _, family := range entry.Details.Families {
			if len(family) > 128 {
				return Error{InvalidResponse}
			}
		}
		seen[entry.Name] = true
		if entry.Name == model {
			if entry.Model != model || entry.Digest != digest || entry.Details.Format != "gguf" || strings.Contains(strings.ToLower(entry.Model), "cloud") {
				return Error{ModelMismatch}
			}
			found = true
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim(']') {
		return Error{InvalidResponse}
	}
	if _, err := d.Token(); err != io.EOF {
		return Error{InvalidResponse}
	}
	if !found {
		return Error{ModelMismatch}
	}
	return nil
}

func decodeEmbedding(body []byte, model string, count, dimensions int) ([][]float32, error) {
	var outer struct {
		Model           string          `json:"model"`
		Embeddings      json.RawMessage `json:"embeddings"`
		TotalDuration   int64           `json:"total_duration,omitempty"`
		LoadDuration    int64           `json:"load_duration,omitempty"`
		PromptEvalCount int64           `json:"prompt_eval_count,omitempty"`
	}
	if authority.DecodeJSON(body, &outer) != nil || outer.TotalDuration < 0 || outer.LoadDuration < 0 || outer.PromptEvalCount < 0 {
		return nil, Error{InvalidResponse}
	}
	if outer.Model != model {
		return nil, Error{ModelMismatch}
	}
	if count < 1 || count > maxBatch || dimensions < 1 || dimensions > 4096 {
		return nil, Error{InvalidResponse}
	}
	d := json.NewDecoder(bytes.NewReader(outer.Embeddings))
	d.UseNumber()
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return nil, Error{InvalidResponse}
	}
	vectors := make([][]float32, 0, count)
	for d.More() {
		if len(vectors) == count {
			return nil, Error{InvalidResponse}
		}
		if token, err := d.Token(); err != nil || token != json.Delim('[') {
			return nil, Error{InvalidResponse}
		}
		vector := make([]float32, 0, dimensions)
		var sum float64
		for d.More() {
			if len(vector) == dimensions {
				return nil, Error{InvalidResponse}
			}
			token, err := d.Token()
			number, ok := token.(json.Number)
			if err != nil || !ok {
				return nil, Error{InvalidResponse}
			}
			value, err := number.Float64()
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat32 {
				return nil, Error{InvalidResponse}
			}
			component := float32(value)
			sum += float64(component) * float64(component)
			vector = append(vector, component)
		}
		if token, err := d.Token(); err != nil || token != json.Delim(']') || len(vector) != dimensions || sum == 0 {
			return nil, Error{InvalidResponse}
		}
		norm := math.Sqrt(sum)
		for i := range vector {
			vector[i] = float32(float64(vector[i]) / norm)
		}
		vectors = append(vectors, vector)
	}
	if token, err := d.Token(); err != nil || token != json.Delim(']') || len(vectors) != count {
		return nil, Error{InvalidResponse}
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, Error{InvalidResponse}
	}
	return vectors, nil
}
