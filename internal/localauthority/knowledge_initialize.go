package localauthority

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"gitcode-mcp/internal/authority"
)

type KnowledgeSetupResult struct {
	Schema     int            `json:"schema"`
	ProjectID  authority.UUID `json:"project_id"`
	InstanceID authority.UUID `json:"instance_id"`
	State      string         `json:"state"`
	Durability string         `json:"durability"`
	Replayed   bool           `json:"replayed"`
}

func (r KnowledgeSetupResult) Validate() error {
	if r.Schema != 1 || !r.ProjectID.Valid() || !r.InstanceID.Valid() || r.State != "ready" || r.Durability != authority.OrderedOSBarriersV1 {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

// SetupKnowledge is an explicit trusted service action, not a read, migration,
// cache promotion, runtime enrollment or transport advertisement. Its original
// caller key owns every admitted phase; unbound or foreign evidence is retained.
func (s *Store) SetupKnowledge(ctx context.Context, project authority.UUID, key string) (KnowledgeSetupResult, error) {
	var zero KnowledgeSetupResult
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if (authority.WriteIntent{ProjectID: project, WriteMode: "local", IdempotencyKey: key}).Validate(true) != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if s.closed || s.lease == nil {
		return zero, Error{ReadOnly}
	}
	if project != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if s.schema == 1 {
		return zero, Error{KnowledgeMigrationRequired}
	}
	if s.logicalFenced {
		return zero, Error{Corrupt}
	}
	if err := s.verifyPhysicalBinding(); err != nil {
		s.fenced = true
		return zero, err
	}
	if err := verifyWriter(ctx, s.db); err != nil {
		return zero, err
	}
	keyHash := hash([]byte(string(project) + "\x00" + key))
	k, err := loadKnowledgeSetup(ctx, s.db, project, s.instance, s.directory)
	replay := err == nil
	if replay {
		if k.Key != keyHash {
			return zero, authority.Error{Code: authority.Conflict}
		}
		if err = s.verifyIdentity(ctx, project); err != nil {
			s.fenced = true
			return zero, err
		}
		if err = s.publishBarrier(); err != nil {
			s.fenced = true
			return zero, err
		}
		s.fenced = false
	} else if !errors.Is(err, sql.ErrNoRows) {
		var e Error
		if errors.As(err, &e) && e.Code == Corrupt {
			s.fenced, s.logicalFenced = true, true
		}
		return zero, err
	} else {
		if s.fenced {
			return zero, Error{ReadOnly}
		}
		var used int
		if s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM receipts WHERE key_hash=?)+(SELECT count(*) FROM document_operations WHERE key_hash=?)`, keyHash, keyHash).Scan(&used) != nil {
			return zero, storageError(ctx)
		}
		if used != 0 {
			return zero, authority.Error{Code: authority.Conflict}
		}
		path := filepath.Join(s.directory, knowledgeDirectoryName)
		if _, e := os.Lstat(path); e == nil {
			return zero, Error{KnowledgeCorrupt}
		} else if !os.IsNotExist(e) {
			return zero, Error{DurabilityUnavailable}
		}
		instance, e := authority.NewUUID()
		if e != nil {
			return zero, e
		}
		k = knowledgeSetupRecord{Project: project, Tracker: s.instance, Instance: instance,
			Coordinate: hash([]byte(path)), Config: hash([]byte(knowledgeConfig + knowledgeHEAD)), Algorithm: knowledgeAlgorithm,
			Key: keyHash, Phase: "reserved", Generation: 1}
		k.Intent = k.intentHash()
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return zero, storageError(ctx)
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `INSERT INTO knowledge_setup VALUES(1,?,?,?,?,?,?,?,?,?,?,?,?)`, k.Project, k.Tracker, k.Instance, k.Coordinate, k.Physical, k.Binding, k.Config, k.Algorithm, k.Key, k.Intent, k.Phase, k.Generation); e != nil {
			return zero, storageError(ctx)
		}
		if e = s.commitKnowledge(ctx, tx, "knowledge_setup_reserved"); e != nil {
			return zero, e
		}
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	g := s.knowledgeRepository(k)
	if k.Phase == "reserved" {
		if _, e := os.Lstat(g.directory); !os.IsNotExist(e) {
			// No original physical proof is durable yet. Shape, name or a
			// different caller key cannot establish ownership of an orphan.
			return zero, Error{KnowledgeInitializationPending}
		}
		if os.Mkdir(g.directory, 0700) != nil || syncDirectory(s.directory) != nil {
			return zero, Error{KnowledgeInitializationPending}
		}
		if err = s.inject("knowledge_setup_unbound_directory"); err != nil {
			return zero, err
		}
		physical, e := knowledgeDirectoryIdentity(g.directory)
		if e != nil {
			return zero, e
		}
		k.Physical, k.Phase, k.Generation = physical, "bound", 2
		k.Binding = k.bindingHash()
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return zero, storageError(ctx)
		}
		defer tx.Rollback()
		result, e := tx.ExecContext(ctx, `UPDATE knowledge_setup SET physical_hash=?,binding_hash=?,phase='bound',generation=2 WHERE singleton=1 AND phase='reserved' AND generation=1 AND key_hash=? AND intent_hash=?`, k.Physical, k.Binding, k.Key, k.Intent)
		if e != nil {
			return zero, storageError(ctx)
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return zero, Error{KnowledgeInitializationPending}
		}
		if e = s.commitKnowledge(ctx, tx, "knowledge_setup_bound"); e != nil {
			return zero, e
		}
		g = s.knowledgeRepository(k)
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err = g.verifyPhysical(); err != nil {
		return zero, err
	}
	if err = qualifyFilesystem(g.directory); err != nil {
		return zero, err
	}
	if k.Phase == "bound" {
		if err = g.populate(); err != nil {
			return zero, err
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return zero, storageError(ctx)
		}
		defer tx.Rollback()
		result, e := tx.ExecContext(ctx, `UPDATE knowledge_setup SET phase='ready',generation=3 WHERE singleton=1 AND phase='bound' AND generation=2 AND key_hash=? AND binding_hash=?`, k.Key, k.Binding)
		if e != nil {
			return zero, storageError(ctx)
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return zero, Error{KnowledgeInitializationPending}
		}
		if e = s.commitKnowledge(ctx, tx, "knowledge_setup_ready"); e != nil {
			return zero, e
		}
	}
	if _, _, err = g.verifyLayout(); err != nil {
		return zero, err
	}
	// Replaying a ready row is not a fresh durability assertion based solely
	// on equivalent bytes: requalify the original setup metadata/barriers.
	if err = g.syncSetupMetadata(); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	return KnowledgeSetupResult{Schema: 1, ProjectID: project, InstanceID: k.Instance, State: "ready", Durability: authority.OrderedOSBarriersV1, Replayed: replay}, nil
}

func (s *Store) commitKnowledge(ctx context.Context, tx *sql.Tx, phase string) error {
	if err := tx.Commit(); err != nil {
		s.fenced = true
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return Error{CommitUncertain}
	}
	if err := s.publishBarrier(); err != nil {
		s.fenced = true
		return err
	}
	return s.inject(phase)
}
