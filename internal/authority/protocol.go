package authority

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

type NegotiationRequest struct {
	Protocol  string `json:"protocol"`
	Authority Kind   `json:"authority"`
	SchemaMin int    `json:"schema_min"`
	SchemaMax int    `json:"schema_max"`
}

func (n NegotiationRequest) Validate() error {
	if n.Protocol != Protocol || n.SchemaMin < 1 || n.SchemaMax < n.SchemaMin || n.SchemaMin > Schema || n.SchemaMax < Schema {
		return Error{Unsupported}
	}
	if n.Authority != Local {
		return Error{InvalidContract}
	}
	return nil
}

type Negotiation struct {
	Protocol          string   `json:"protocol"`
	SchemaMin         int      `json:"schema_min"`
	SchemaMax         int      `json:"schema_max"`
	Authority         Kind     `json:"authority"`
	Available         bool     `json:"available"`
	Methods           []string `json:"methods"`
	Reason            Code     `json:"reason"`
	Handoff           string   `json:"handoff"`
	DocumentSchemaMin int      `json:"document_schema_min,omitempty"`
	DocumentSchemaMax int      `json:"document_schema_max,omitempty"`
}

// ContractsOnly is honest negotiation until storage, single-writer runtime and
// workflow cohorts are implemented. It grants no read/write work admission.
func ContractsOnly() Negotiation {
	return Negotiation{Protocol: Protocol, SchemaMin: Schema, SchemaMax: Schema, Authority: Local, Available: false, Methods: []string{}, Reason: Unavailable, Handoff: "local project runtime and workflows are not available in this build"}
}

type ReadRequest struct {
	ProjectID UUID       `json:"project_id"`
	Kind      EntityKind `json:"kind,omitempty"`
	EntityID  UUID       `json:"entity_id,omitempty"`
	Revision  uint64     `json:"revision,omitempty"`
	Operation string     `json:"operation"`
	Limit     int        `json:"limit,omitempty"`
	Cursor    string     `json:"cursor,omitempty"`
}

func (r ReadRequest) Validate() error {
	if !r.ProjectID.Valid() || r.Limit < 0 || r.Limit > 100 || len(r.Cursor) > 512 {
		return Error{InvalidContract}
	}
	switch r.Operation {
	case "status":
		if r.Kind != "" || r.EntityID != "" || r.Revision != 0 || r.Limit != 0 || r.Cursor != "" {
			return Error{InvalidContract}
		}
	case "list":
		if !r.Kind.Valid() || r.EntityID != "" || r.Revision != 0 {
			return Error{InvalidContract}
		}
	case "get", "history":
		if !r.Kind.Valid() || !r.EntityID.Valid() {
			return Error{InvalidContract}
		}
		if r.Operation == "get" && (r.Cursor != "" || r.Limit != 0) || r.Operation == "history" && r.Revision != 0 {
			return Error{InvalidContract}
		}
	default:
		return Error{Unsupported}
	}
	return nil
}

// WriteIntent is separate from remote write_mode=live. A local receipt means a
// confirmed authority commit, never a provider-confirmed remote operation.
type WriteIntent struct {
	ProjectID        UUID   `json:"project_id"`
	WriteMode        string `json:"write_mode"`
	IdempotencyKey   string `json:"idempotency_key"`
	EntityID         UUID   `json:"entity_id,omitempty"`
	ExpectedRevision uint64 `json:"expected_revision,omitempty"`
}

func (w WriteIntent) Validate(create bool) error {
	if !w.ProjectID.Valid() || w.WriteMode != "local" || len(w.IdempotencyKey) < 1 || len(w.IdempotencyKey) > 128 || !utf8.ValidString(w.IdempotencyKey) || strings.TrimSpace(w.IdempotencyKey) != w.IdempotencyKey {
		return Error{InvalidContract}
	}
	for _, c := range w.IdempotencyKey {
		if c < 32 || c == 127 {
			return Error{InvalidContract}
		}
	}
	if create {
		if w.EntityID != "" || w.ExpectedRevision != 0 {
			return Error{InvalidContract}
		}
	} else if !w.EntityID.Valid() || w.ExpectedRevision == 0 {
		return Error{InvalidContract}
	}
	return nil
}

type MutationRequest struct {
	Intent    WriteIntent     `json:"intent"`
	Operation string          `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
}

type IssuePayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	State string `json:"state"`
}
type CommentPayload struct {
	Issue Citation `json:"issue"`
	Body  string   `json:"body"`
}
type LabelPayload struct {
	Name     string `json:"name"`
	Color    string `json:"color"`
	Archived bool   `json:"archived"`
}
type MilestonePayload struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	DueDate     string `json:"due_date"`
	Archived    bool   `json:"archived"`
}
type DocumentPayload struct {
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}
type LinkPayload struct {
	From     Citation `json:"from"`
	To       Citation `json:"to"`
	Relation string   `json:"relation"`
}

// Payload bytes travel only in protected local request bodies. Store adapters
// validate entity-specific lifecycle/relationship rules before transaction;
// L1 establishes syntactic bounds and project-scoped reference identity.
func (m MutationRequest) Validate() error {
	parts := strings.Split(m.Operation, ".")
	if len(parts) != 2 {
		return Error{Unsupported}
	}
	create := parts[1] == "create"
	if err := m.Intent.Validate(create); err != nil {
		return err
	}
	if parts[1] != "create" && parts[1] != "update" && parts[1] != "tombstone" {
		return Error{Unsupported}
	}
	if parts[1] == "tombstone" {
		if parts[0] != "document" && parts[0] != "link" {
			return Error{Unsupported}
		}
		return DecodeJSON(m.Payload, &struct{}{})
	}
	text := func(s string, max int, required bool) bool {
		return utf8.ValidString(s) && len(s) <= max && (!required || strings.TrimSpace(s) != "")
	}
	sameProject := func(c Citation) bool { return c.Validate() == nil && c.ProjectID == m.Intent.ProjectID }
	switch parts[0] {
	case "issue":
		var p IssuePayload
		if err := DecodeJSON(m.Payload, &p); err != nil {
			return err
		}
		if !text(p.Title, 256, true) || !text(p.Body, 128<<10, false) || p.State != "open" && p.State != "closed" {
			return Error{InvalidContract}
		}
	case "comment":
		var p CommentPayload
		if err := DecodeJSON(m.Payload, &p); err != nil {
			return err
		}
		if !sameProject(p.Issue) || p.Issue.Kind != Issue || !text(p.Body, 128<<10, true) {
			return Error{InvalidContract}
		}
	case "label":
		var p LabelPayload
		if err := DecodeJSON(m.Payload, &p); err != nil {
			return err
		}
		if !text(p.Name, 128, true) || len(p.Color) != 7 || p.Color[0] != '#' {
			return Error{InvalidContract}
		}
		for _, c := range p.Color[1:] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return Error{InvalidContract}
			}
		}
	case "milestone":
		var p MilestonePayload
		if err := DecodeJSON(m.Payload, &p); err != nil {
			return err
		}
		if !text(p.Title, 256, true) || !text(p.Description, 128<<10, false) || len(p.DueDate) > 10 {
			return Error{InvalidContract}
		}
		if p.DueDate != "" {
			if _, err := time.Parse("2006-01-02", p.DueDate); err != nil {
				return Error{InvalidContract}
			}
		}
	case "document":
		var p DocumentPayload
		if err := DecodeJSON(m.Payload, &p); err != nil {
			return err
		}
		if !text(p.Title, 256, true) || !text(p.Markdown, 256<<10, true) {
			return Error{InvalidContract}
		}
	case "link":
		var p LinkPayload
		if err := DecodeJSON(m.Payload, &p); err != nil {
			return err
		}
		if !sameProject(p.From) || !sameProject(p.To) {
			return Error{InvalidContract}
		}
		switch p.Relation {
		case "relates_to", "blocks", "evidence_for", "label", "milestone":
		default:
			return Error{InvalidContract}
		}
	default:
		return Error{Unsupported}
	}
	return nil
}
