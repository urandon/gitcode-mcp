package authority

import "encoding/json"

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

type DocumentLocator struct {
	Citation   Citation `json:"citation"`
	CommitOID  string   `json:"commit_oid"`
	BlobOID    string   `json:"blob_oid"`
	SHA256     string   `json:"sha256"`
	ByteLength int64    `json:"byte_length"`
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
