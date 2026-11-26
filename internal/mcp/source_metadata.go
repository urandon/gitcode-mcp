package mcp

import (
	"bytes"
	"encoding/json"
)

func validSourceMetadataJSON(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for _, name := range []string{"labels", "labels_match", "title_contains", "status"} {
		if value, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}
