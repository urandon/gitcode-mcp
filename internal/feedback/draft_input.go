package feedback

import (
	"bytes"
	"encoding/json"
)

const MaxDraftInputBytes = 1 << 20

// DecodeDraftJSON accepts report data only, not a rendered body, destination,
// fingerprint or readiness receipt. All fields are revalidated by Prepare.
func DecodeDraftJSON(data []byte) (Draft, error) {
	var draft Draft
	if len(data) > MaxDraftInputBytes || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return draft, ValidationError{Field: "draft", Message: "must be a JSON object of at most 1 MiB"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&draft) != nil || !json.Valid(data) {
		return Draft{}, ValidationError{Field: "draft", Message: "must contain only known feedback draft fields with their expected types"}
	}
	return draft, nil
}

// DecodeDraftInput also accepts the complete JSON output of prepare. Only its
// sanitized draft is used; all rendered text and claimed evidence are ignored.
func DecodeDraftInput(data []byte) (Draft, error) {
	if len(data) > MaxDraftInputBytes {
		return Draft{}, ValidationError{Field: "input", Message: "feedback input exceeds 1 MiB"}
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil || envelope == nil {
		return Draft{}, ValidationError{Field: "input", Message: "feedback input must be a JSON object"}
	}
	draft, wrapped := envelope["draft"]
	if !wrapped {
		return DecodeDraftJSON(data)
	}
	allowed := map[string]bool{"draft": true, "status": true, "configured": true, "sink": true, "repo_id": true, "title": true, "body": true, "fingerprint": true, "dedupe_decision": true, "candidates": true, "context": true, "redactions_applied": true, "missing_fields": true, "follow_up_questions": true, "remediation": true, "readiness": true}
	for key := range envelope {
		if !allowed[key] {
			return Draft{}, ValidationError{Field: "input", Message: "prepared output cannot be mixed with flat draft fields or unknown fields"}
		}
	}
	return DecodeDraftJSON(draft)
}
