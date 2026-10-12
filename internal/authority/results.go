package authority

import (
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var displayAlias = regexp.MustCompile(`^(ISSUE|COMMENT|LABEL|MILESTONE|DOC|LINK)-([1-9][0-9]*)$`)

// Result contracts are consumed by the storage/runtime/workflow cohorts. L1
// never constructs a confirmed receipt or advertises these workflows available.
type EntityView struct {
	Citation        Citation        `json:"citation"`
	DisplayAlias    string          `json:"display_alias"`
	ProjectSequence uint64          `json:"project_sequence"`
	Tombstone       bool            `json:"tombstone"`
	Payload         json.RawMessage `json:"payload"`
}

type ReadResult struct {
	ProjectID  UUID         `json:"project_id"`
	Entities   []EntityView `json:"entities"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

func (v EntityView) Validate() error {
	if v.Citation.Validate() != nil || v.ProjectSequence == 0 {
		return Error{InvalidContract}
	}
	match := displayAlias.FindStringSubmatch(v.DisplayAlias)
	prefix := map[EntityKind]string{Issue: "ISSUE", Comment: "COMMENT", Label: "LABEL", Milestone: "MILESTONE", Document: "DOC", Link: "LINK"}
	if len(match) != 3 || match[1] != prefix[v.Citation.Kind] {
		return Error{InvalidContract}
	}
	if _, err := strconv.ParseUint(match[2], 10, 64); err != nil {
		return Error{InvalidContract}
	}
	if v.Tombstone {
		if v.Citation.Kind != Document && v.Citation.Kind != Link {
			return Error{InvalidContract}
		}
		return DecodeJSON(v.Payload, &struct{}{})
	}
	// The same concrete full-replacement entity payload schemas apply on reads.
	// This validates shape/reference namespace, not an unperformed transaction.
	return (MutationRequest{Intent: WriteIntent{ProjectID: v.Citation.ProjectID, WriteMode: "local", IdempotencyKey: "schema-validation"}, Operation: string(v.Citation.Kind) + ".create", Payload: v.Payload}).Validate()
}

func (r ReadResult) Validate() error {
	if !r.ProjectID.Valid() || r.Entities == nil || len(r.Entities) > 100 || !utf8.ValidString(r.NextCursor) || len(r.NextCursor) > 512 || len(r.Entities) == 0 && r.NextCursor != "" {
		return Error{InvalidContract}
	}
	seen := map[Citation]bool{}
	for _, v := range r.Entities {
		if v.Validate() != nil || v.Citation.ProjectID != r.ProjectID || seen[v.Citation] {
			return Error{InvalidContract}
		}
		seen[v.Citation] = true
	}
	return nil
}

type DocumentLocator struct {
	Citation   Citation `json:"citation"`
	CommitOID  string   `json:"commit_oid"`
	BlobOID    string   `json:"blob_oid"`
	SHA256     string   `json:"sha256"`
	ByteLength int64    `json:"byte_length"`
}

func digestValid(s string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		validLength = validLength || len(s) == length
	}
	if !validLength || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (d DocumentLocator) Validate() error {
	if d.Citation.Validate() != nil || d.Citation.Kind != Document || !digestValid(d.CommitOID, 40, 64) || !digestValid(d.BlobOID, 40, 64) || len(d.CommitOID) != len(d.BlobOID) || !digestValid(d.SHA256, 64) || d.ByteLength < 1 || d.ByteLength > 256<<10 {
		return Error{InvalidContract}
	}
	return nil
}

type MutationReceipt struct {
	ProjectID          UUID     `json:"project_id"`
	OperationID        UUID     `json:"operation_id"`
	Citation           Citation `json:"citation"`
	ProjectSequence    uint64   `json:"project_sequence"`
	IdempotencyKeyHash string   `json:"idempotency_key_hash"`
	IntentHash         string   `json:"intent_hash"`
	Evidence           string   `json:"evidence"` // local_commit_confirmed, never provider-confirmed
	Replayed           bool     `json:"replayed"`
	ProjectionState    string   `json:"projection_state"`
}

func (r MutationReceipt) Validate() error {
	if !r.ProjectID.Valid() || !r.OperationID.Valid() || r.Citation.Validate() != nil || r.Citation.ProjectID != r.ProjectID || r.ProjectSequence == 0 || !digestValid(r.IdempotencyKeyHash, 64) || !digestValid(r.IntentHash, 64) || r.Evidence != "local_commit_confirmed" {
		return Error{InvalidContract}
	}
	switch r.ProjectionState {
	case "pending", "ready", "degraded":
	default:
		return Error{InvalidContract}
	}
	return nil
}

type ProjectStatus struct {
	ProjectID           UUID   `json:"project_id"`
	Authority           Kind   `json:"authority"`
	Readiness           string `json:"readiness"`
	Generation          uint64 `json:"generation"`
	NetworkState        string `json:"network_state"`
	WriterState         string `json:"writer_state"`
	AuthorityGeneration uint64 `json:"authority_generation"`
	IndexedGeneration   uint64 `json:"indexed_generation"`
}

func (s ProjectStatus) Validate() error {
	if !s.ProjectID.Valid() || s.Authority != Local || s.Generation == 0 || s.IndexedGeneration > s.AuthorityGeneration {
		return Error{InvalidContract}
	}
	switch s.Readiness {
	case "uninitialized", "ready", "read_only", "restoring", "recovery_required", "archived":
	default:
		return Error{InvalidContract}
	}
	switch s.WriterState {
	case "unavailable", "owned", "conflict", "quiesced":
	default:
		return Error{InvalidContract}
	}
	switch s.NetworkState {
	case "zero_network", "loopback_control_only", "loopback_provider_network":
	default:
		return Error{InvalidContract}
	}
	return nil
}

type OperationContract struct {
	Operation string
	Entity    EntityKind
	CLI       string
	MCP       string
	CAS       bool
}

// OperationContracts specifies final names for L5/L6 without adding entries to
// current tool discovery. Copies protect the contract inventory from callers.
func OperationContracts() []OperationContract {
	return []OperationContract{
		{"issue.create", Issue, "project issue create", "project_create_issue", false},
		{"issue.update", Issue, "project issue update", "project_update_issue", true},
		{"comment.create", Comment, "project comment create", "project_add_comment", false},
		{"comment.update", Comment, "project comment update", "project_update_comment", true},
		{"label.create", Label, "project label create", "project_create_label", false},
		{"label.update", Label, "project label update", "project_update_label", true},
		{"milestone.create", Milestone, "project milestone create", "project_create_milestone", false},
		{"milestone.update", Milestone, "project milestone update", "project_update_milestone", true},
		{"document.create", Document, "project document create", "project_create_document", false},
		{"document.update", Document, "project document update", "project_update_document", true},
		{"document.tombstone", Document, "project document tombstone", "project_tombstone_document", true},
		{"link.create", Link, "project link create", "project_create_link", false},
		{"link.update", Link, "project link update", "project_update_link", true},
		{"link.tombstone", Link, "project link tombstone", "project_tombstone_link", true},
	}
}
