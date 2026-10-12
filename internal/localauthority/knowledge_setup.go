package localauthority

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"

	"gitcode-mcp/internal/authority"
)

const knowledgeDirectoryName = "knowledge.git"

// This file only validates/reads setup records. It neither creates Git state
// nor grants mutation admission. Physical Git readiness is separate from SQL
// integrity so damaged document storage cannot disable an intact tracker.
type knowledgeSetupRecord struct {
	Project, Tracker, Instance            authority.UUID
	Coordinate, Physical, Binding, Config string
	Algorithm                             int
	Key, Intent, Phase                    string
	Generation                            int
}

func (k knowledgeSetupRecord) intentHash() string {
	body, _ := json.Marshal(struct {
		Schema                     int
		Project, Tracker, Instance authority.UUID
		Coordinate, Config         string
		Algorithm                  int
	}{1, k.Project, k.Tracker, k.Instance, k.Coordinate, k.Config, k.Algorithm})
	return hash(body)
}

func (k knowledgeSetupRecord) bindingHash() string {
	body, _ := json.Marshal(struct {
		Schema                                    int
		Project, Tracker, Instance                authority.UUID
		Coordinate, Physical, Config, Key, Intent string
		Algorithm                                 int
	}{1, k.Project, k.Tracker, k.Instance, k.Coordinate, k.Physical, k.Config, k.Key, k.Intent, k.Algorithm})
	return hash(body)
}

const knowledgeSetupQuery = `SELECT
 CASE WHEN length(project_id)=36 THEN project_id END,
 CASE WHEN length(tracker_instance)=36 THEN tracker_instance END,
 CASE WHEN length(knowledge_instance)=36 THEN knowledge_instance END,
 CASE WHEN length(coordinate_hash)=64 THEN coordinate_hash END,
 CASE WHEN length(physical_hash) IN(0,64) THEN physical_hash END,
 CASE WHEN length(binding_hash) IN(0,64) THEN binding_hash END,
 CASE WHEN length(config_hash)=64 THEN config_hash END,
 CASE WHEN typeof(algorithm)='integer' AND algorithm=1 THEN algorithm END,
 CASE WHEN length(key_hash)=64 THEN key_hash END,
 CASE WHEN length(intent_hash)=64 THEN intent_hash END,
 CASE WHEN length(phase)<=8 THEN phase END,
 CASE WHEN typeof(generation)='integer' AND generation BETWEEN 1 AND 3 THEN generation END
 FROM knowledge_setup WHERE singleton=1`

func loadKnowledgeSetup(ctx context.Context, db receiptReader, project, tracker authority.UUID, directory string) (knowledgeSetupRecord, error) {
	var k knowledgeSetupRecord
	if ctx.Err() != nil {
		return k, ctx.Err()
	}
	err := db.QueryRowContext(ctx, knowledgeSetupQuery).Scan(&k.Project, &k.Tracker, &k.Instance, &k.Coordinate, &k.Physical, &k.Binding, &k.Config, &k.Algorithm, &k.Key, &k.Intent, &k.Phase, &k.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return k, err
	}
	if err != nil && ctx.Err() != nil {
		return k, ctx.Err()
	}
	if err != nil || !k.Instance.Valid() || k.Project != project || k.Tracker != tracker || k.Coordinate != hash([]byte(filepath.Join(directory, knowledgeDirectoryName))) || k.Config != hash([]byte(knowledgeConfig+knowledgeHEAD)) || k.Algorithm != knowledgeAlgorithm || !validDigest(k.Key) || k.Intent != k.intentHash() {
		return knowledgeSetupRecord{}, Error{Corrupt}
	}
	switch k.Phase {
	case "reserved":
		if k.Generation != 1 || k.Physical != "" || k.Binding != "" {
			return knowledgeSetupRecord{}, Error{Corrupt}
		}
	case "bound", "ready":
		generation := 2
		if k.Phase == "ready" {
			generation = 3
		}
		if k.Generation != generation || !validDigest(k.Physical) || k.Binding != k.bindingHash() {
			return knowledgeSetupRecord{}, Error{Corrupt}
		}
	default:
		return knowledgeSetupRecord{}, Error{Corrupt}
	}
	var collisions int
	if db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM receipts WHERE key_hash=?)+(SELECT count(*) FROM document_operations WHERE key_hash=?)`, k.Key, k.Key).Scan(&collisions) != nil || collisions != 0 {
		if ctx.Err() != nil {
			return knowledgeSetupRecord{}, ctx.Err()
		}
		return knowledgeSetupRecord{}, Error{Corrupt}
	}
	return k, nil
}

// KnowledgeState is protected, content-free readiness; it never reports paths,
// caller keys or arbitrary peer/driver errors. No workflow is advertised by it.
type KnowledgeState struct {
	Schema        int                     `json:"schema"`
	ProjectID     authority.UUID          `json:"project_id"`
	InstanceID    authority.UUID          `json:"instance_id,omitempty"`
	Scope         string                  `json:"scope"`
	Phase         string                  `json:"phase"`
	Code          string                  `json:"code,omitempty"`
	Remediation   string                  `json:"remediation"`
	Durability    string                  `json:"durability,omitempty"`
	Capacity      string                  `json:"capacity,omitempty"`
	Pending       *DocumentOperationState `json:"pending,omitempty"`
	CurrentRefOID string                  `json:"current_ref_oid,omitempty"`
}

func (s KnowledgeState) Validate() error {
	if s.Schema != 1 || !s.ProjectID.Valid() || s.Scope != "knowledge_setup" {
		return authority.Error{Code: authority.InvalidContract}
	}
	if s.Phase == "ready" {
		if !s.InstanceID.Valid() || s.Code != "" || s.Remediation != "none" || s.Durability != authority.OrderedOSBarriersV1 || s.Capacity != "" || !knowledgeOIDValid(s.CurrentRefOID, true) {
			return authority.Error{Code: authority.InvalidContract}
		}
		if s.Pending != nil && (s.Pending.Validate() != nil || s.Pending.ProjectID != s.ProjectID || s.Pending.Phase == "settled") {
			return authority.Error{Code: authority.InvalidContract}
		}
		return nil
	}
	if s.Pending != nil && (s.Phase != "recovery_required" || s.Pending.Validate() != nil || s.Pending.ProjectID != s.ProjectID || s.Pending.Phase == "settled") {
		return authority.Error{Code: authority.InvalidContract}
	}
	if s.CurrentRefOID != "" {
		return authority.Error{Code: authority.InvalidContract}
	}
	if s.Durability != "" {
		return authority.Error{Code: authority.InvalidContract}
	}
	wantCode, wantRemediation := "", ""
	switch s.Phase {
	case "uninitialized":
		wantCode, wantRemediation = string(KnowledgeSetupRequired), "setup"
	case "migration_required":
		wantCode, wantRemediation = string(KnowledgeMigrationRequired), "migrate"
	case "reserved", "bound":
		wantCode, wantRemediation = string(KnowledgeInitializationPending), "recover_original_setup"
	case "corrupt":
		wantCode, wantRemediation = string(KnowledgeCorrupt), "operator_recovery"
	case "recovery_required":
		wantCode, wantRemediation = string(DocumentRecoveryRequired), "operator_recovery"
	case "capacity":
		wantCode, wantRemediation = string(Capacity), "inspect_retained_evidence"
	default:
		return authority.Error{Code: authority.InvalidContract}
	}
	missing := s.Phase == "uninitialized" || s.Phase == "migration_required"
	if s.Code != wantCode || s.Remediation != wantRemediation || missing && s.InstanceID != "" || !missing && !s.InstanceID.Valid() || s.Phase != "capacity" && s.Capacity != "" || s.Phase == "capacity" && s.Capacity != "retained_pins" && s.Capacity != "loose_objects" {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func (s *Store) knowledgeRepository(k knowledgeSetupRecord) knowledgeRepository {
	return knowledgeRepository{directory: filepath.Join(s.directory, knowledgeDirectoryName), physical: k.Physical, fault: s.fault}
}

// KnowledgeStatus never initializes Git, migrates SQL or changes a registration.
func (s *Store) KnowledgeStatus(ctx context.Context, project authority.UUID) (KnowledgeState, error) {
	var zero KnowledgeState
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if !project.Valid() {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return zero, Error{ReadOnly}
	}
	if project != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	state := KnowledgeState{Schema: 1, ProjectID: project, Scope: "knowledge_setup", Phase: "uninitialized", Remediation: "setup"}
	if s.schema == 1 {
		state.Phase, state.Code, state.Remediation = "migration_required", string(KnowledgeMigrationRequired), "migrate"
		return state, nil
	}
	k, err := loadKnowledgeSetup(ctx, s.db, project, s.instance, s.directory)
	if errors.Is(err, sql.ErrNoRows) {
		state.Code = string(KnowledgeSetupRequired)
		return state, nil
	}
	if err != nil {
		var e Error
		if errors.As(err, &e) && e.Code == Corrupt {
			s.fenced = true
			s.logicalFenced = true
		}
		return zero, err
	}
	state.InstanceID, state.Phase = k.Instance, k.Phase
	if k.Phase != "ready" {
		state.Code, state.Remediation = string(KnowledgeInitializationPending), "recover_original_setup"
		return state, nil
	}
	g := s.knowledgeRepository(k)
	pending, err := s.pendingDocumentOperation(ctx)
	if err != nil {
		return zero, err
	}
	_, _, err = g.verifyJournalLayout(pending)
	if err == nil {
		err = s.verifyDocumentInventory(ctx, g)
	}
	if err == nil && pending == nil {
		_, err = s.verifySettledHead(ctx, g)
	}
	current := ""
	if err == nil {
		current, err = g.readRef(filepath.Join("heads", "knowledge"))
	}
	if err != nil {
		state.Phase, state.Code, state.Remediation = "corrupt", string(KnowledgeCorrupt), "operator_recovery"
		var e Error
		if errors.As(err, &e) && e.Code == Capacity {
			state.Phase, state.Code, state.Remediation = "capacity", string(Capacity), "inspect_retained_evidence"
			var capacity knowledgeCapacityError
			if errors.As(err, &capacity) {
				state.Capacity = capacity.category
			}
		}
		if errors.As(err, &e) && e.Code == DocumentRecoveryRequired {
			state.Phase, state.Code = "recovery_required", string(DocumentRecoveryRequired)
			if pending != nil {
				operation := operationState(*pending, project)
				state.Pending = &operation
			}
		}
		return state, nil
	}
	state.Remediation, state.Durability = "none", authority.OrderedOSBarriersV1
	state.CurrentRefOID = current
	if pending != nil {
		operation := operationState(*pending, project)
		state.Pending = &operation
	}
	return state, nil
}

// SQL validation only: filesystem absence/damage is deliberately not queried
// during tracker admission. The caller still requires the exact v2 catalog.
func checkKnowledgeSetup(ctx context.Context, tx *sql.Tx, project, tracker authority.UUID, directory string) error {
	_, err := loadKnowledgeSetup(ctx, tx, project, tracker, directory)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
