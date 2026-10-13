package localauthority

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"gitcode-mcp/internal/authority"
	_ "modernc.org/sqlite"
)

type Store struct {
	mu        sync.Mutex
	db        *sql.DB
	lease     *lease
	manifest  authority.Manifest
	instance  authority.UUID
	schema    int
	directory string // private, never serialized
	fenced    bool
	// Logical corruption cannot be cleared by a receipt's physical OS barrier.
	// A verified reopen must validate repaired authority before any fresh ack.
	logicalFenced bool
	closed        bool
	// fault is test-only injection at transaction boundaries; no public option.
	fault func(string) error
}

type Inspection struct {
	ProjectID    authority.UUID `json:"project_id"`
	Schema       int            `json:"schema"`
	PermitWrites bool           `json:"permit_writes"`
	Durability   string         `json:"durability"`
}

// prepareEmpty never publishes project ownership or the final database name.
// Its staging directory is unique to one unacknowledged initialization.
func prepareEmpty(ctx context.Context, directory string, m authority.Manifest, instance authority.UUID, fault func(string) error) (*Store, error) {
	return prepareEmptySchema(ctx, directory, m, instance, fault, schemaVersion)
}

// The public fresh initialization path selects v2 explicitly. Existing v1 is
// opened and validated separately, never passed through this DDL constructor.
func prepareEmptySchema(ctx context.Context, directory string, m authority.Manifest, instance authority.UUID, fault func(string) error, version int) (*Store, error) {
	ddl := authoritySchemaSQL(version)
	if ddl == "" {
		return nil, authority.Error{Code: authority.Unsupported}
	}
	if m.Validate() != nil || !m.ID.Valid() {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	path, err := privateDirectory(directory)
	if err != nil {
		return nil, err
	}
	if err = qualifyFilesystem(path); err != nil {
		return nil, err
	}
	s := &Store{manifest: m, instance: instance, directory: path, fault: fault, schema: version}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	dbPath := filepath.Join(path, "tracker.db")
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, authority.Error{Code: authority.Conflict}
	}
	if err = syncFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	_ = f.Close()
	if err = s.inject("init_file"); err != nil {
		return nil, err
	}
	s.db, err = openDB(ctx, dbPath, false)
	if err != nil {
		return nil, err
	}
	var mode string
	if err = s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
		return nil, Error{DurabilityUnavailable}
	}
	if err = verifyWriter(ctx, s.db); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError(ctx)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return nil, storageError(ctx)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO project VALUES(1,?,?,?,1,0)", m.ID, instance, string(body)); err != nil {
		return nil, storageError(ctx)
	}
	for _, kind := range m.Entities {
		if _, err = tx.ExecContext(ctx, "INSERT INTO aliases VALUES(?,1)", kind); err != nil {
			return nil, storageError(ctx)
		}
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA application_id=1195593025"); err != nil {
		return nil, storageError(ctx)
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA user_version="+strconv.Itoa(version)); err != nil {
		return nil, storageError(ctx)
	}
	if err = s.inject("init_schema"); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, storageError(ctx)
	}
	if err = s.inject("init_commit"); err != nil {
		return nil, err
	}
	// Persist the dedicated header identity before any later read-only probe.
	var busy, pages, checkpointed int
	if err = s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(FULL)").Scan(&busy, &pages, &checkpointed); err != nil || busy != 0 || pages != checkpointed {
		return nil, Error{DurabilityUnavailable}
	}
	if err = s.inject("init_checkpoint"); err != nil {
		return nil, err
	}
	if err = syncDirectory(path); err != nil {
		return nil, err
	}
	if err = s.publishBarrier(); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

// OpenWriter does not initialize, migrate, or select authority by cache config.
// The expected UUID is mandatory; a globally bound physical clone is refused.
func OpenWriter(ctx context.Context, registry *Registry, directory string, expected authority.UUID) (*Store, error) {
	if registry == nil || !expected.Valid() {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	if exists, err := registry.exists(initName(expected)); err != nil {
		return nil, err
	} else if exists {
		return nil, Error{InitializationPending}
	}
	s, err := OpenReadOnly(ctx, directory, expected)
	if err != nil {
		return nil, err
	}
	path, err := privateDirectory(directory)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	if err = qualifyFilesystem(path); err != nil {
		_ = s.Close()
		return nil, err
	}
	owner, err := registry.acquire(ctx, expected, s.instance, path)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	_ = s.db.Close()
	s.db = nil
	s.lease = owner
	if err = owner.bindPhysical(filepath.Join(path, "tracker.db")); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.db, err = openDB(ctx, filepath.Join(path, "tracker.db"), false)
	if err == nil {
		err = verifyWriter(ctx, s.db)
	}
	if err == nil {
		err = s.verifyIdentity(ctx, expected)
	}
	if err == nil {
		err = s.publishBarrier()
	}
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func OpenReadOnly(ctx context.Context, directory string, expected authority.UUID) (*Store, error) {
	if !expected.Valid() {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	path, err := privateDirectory(directory)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(path, "tracker.db")
	if err = verifyHeader(dbPath); err != nil {
		return nil, err
	}
	db, err := openDB(ctx, dbPath, true)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, directory: path}
	if err = s.verifyIdentity(ctx, expected); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func openDB(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(1000)")
	if readOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "synchronous(FULL)")
		q.Add("_pragma", "fullfsync(1)")
		q.Add("_pragma", "checkpoint_fullfsync(1)")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, storageError(ctx)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, storageError(ctx)
	}
	return db, nil
}

func verifyWriter(ctx context.Context, db *sql.DB) error {
	var mode string
	var full, fk, fullfsync int
	if db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode) != nil || mode != "wal" || db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&full) != nil || full != 2 || db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk) != nil || fk != 1 || db.QueryRowContext(ctx, "PRAGMA fullfsync").Scan(&fullfsync) != nil || fullfsync != 1 {
		return Error{DurabilityUnavailable}
	}
	return nil
}

func (s *Store) verifyIdentity(ctx context.Context, expected authority.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(ctx)
	}
	defer tx.Rollback()
	var app, version int
	if tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app) != nil || app != applicationID {
		return Error{Corrupt}
	}
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil {
		return Error{Corrupt}
	}
	if authoritySchemaSQL(version) == "" {
		return authority.Error{Code: authority.Unsupported}
	}
	if s.schema != 0 && s.schema != version {
		return Error{Corrupt}
	}
	if err := checkSchema(ctx, tx, version); err != nil {
		return err
	}
	var id, instance, manifest string
	if tx.QueryRowContext(ctx, "SELECT project_id,instance_id,CASE WHEN length(CAST(manifest AS BLOB))<=4096 THEN manifest END FROM project WHERE singleton=1").Scan(&id, &instance, &manifest) != nil {
		return Error{Corrupt}
	}
	var m authority.Manifest
	if authority.DecodeJSON([]byte(manifest), &m) != nil || m.ID != authority.UUID(id) || !authority.UUID(instance).Valid() {
		return Error{Corrupt}
	}
	if m.ID != expected || s.instance != "" && s.instance != authority.UUID(instance) {
		return authority.Error{Code: authority.Conflict}
	}
	var check string
	if tx.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check) != nil || check != "ok" {
		return Error{Corrupt}
	}
	if err := s.checkConsistency(ctx, tx, version); err != nil {
		return err
	}
	if err := checkLogicalRows(ctx, tx, m.ID); err != nil {
		return err
	}
	s.manifest = m
	s.instance = authority.UUID(instance)
	s.schema = version
	if version == 2 {
		if err := checkKnowledgeSetup(ctx, tx, m.ID, authority.UUID(instance), s.directory); err != nil {
			return err
		}
		if err := checkDocumentOperations(ctx, tx, m.ID, authority.UUID(instance), s.directory); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) checkConsistency(ctx context.Context, tx *sql.Tx, version int) error {
	var count, sequence, generation int64
	if tx.QueryRowContext(ctx, "SELECT count(*),sequence,generation FROM project").Scan(&count, &sequence, &generation) != nil || count != 1 || sequence < 0 || generation != sequence+1 {
		return Error{Corrupt}
	}
	for _, table := range []string{"revisions", "receipts", "audit"} {
		if tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count) != nil || count != sequence {
			return Error{Corrupt}
		}
	}
	var first, last int64
	if tx.QueryRowContext(ctx, "SELECT coalesce(min(sequence),0),coalesce(max(sequence),0) FROM revisions").Scan(&first, &last) != nil || last != sequence || sequence > 0 && first != 1 {
		return Error{Corrupt}
	}
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT entity_id FROM revisions GROUP BY entity_id HAVING min(revision)<>1 OR max(revision)<>count(*))`).Scan(&count) != nil || count != 0 {
		return Error{Corrupt}
	}
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM entities e WHERE e.current_revision<>(SELECT max(r.revision) FROM revisions r WHERE r.entity_id=e.entity_id)`).Scan(&count) != nil || count != 0 {
		return Error{Corrupt}
	}
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM aliases").Scan(&count) != nil || count != 6 {
		return Error{Corrupt}
	}
	if version == 1 {
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM aliases a WHERE a.next_number<>1+coalesce((SELECT max(e.alias_number) FROM entities e WHERE e.kind=a.kind),0)`).Scan(&count) != nil || count != 0 {
			return Error{Corrupt}
		}
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM aliases a WHERE a.next_number<>1+(SELECT count(*) FROM entities e WHERE e.kind=a.kind)`).Scan(&count) != nil || count != 0 {
			return Error{Corrupt}
		}
	} else if err := checkReservedAliases(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return storageError(ctx)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		return Error{Corrupt}
	}
	return nil
}

func verifyHeader(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return Error{Corrupt}
	}
	f, err := openExistingFile(path)
	if err != nil {
		return Error{Corrupt}
	}
	defer f.Close()
	var header [100]byte
	if _, err = io.ReadFull(f, header[:]); err != nil || string(header[:16]) != "SQLite format 3\x00" || binary.BigEndian.Uint32(header[68:72]) != applicationID {
		return Error{Corrupt}
	}
	if authoritySchemaSQL(int(binary.BigEndian.Uint32(header[60:64]))) == "" {
		return authority.Error{Code: authority.Unsupported}
	}
	return nil
}

// Acknowledgement includes publication of database/WAL inode names as well as
// the SQLite FULL commit. Replay/reopen re-qualifies a previously uncertain ack.
func (s *Store) publishBarrier() error {
	if err := s.verifyPhysicalBinding(); err != nil {
		return err
	}
	for _, name := range []string{"tracker.db", "tracker.db-wal"} {
		phase := "database_sync"
		if name == "tracker.db-wal" {
			phase = "wal_sync"
		}
		if err := s.inject(phase); err != nil {
			return err
		}
		path := filepath.Join(s.directory, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) && name == "tracker.db-wal" {
			continue
		}
		f, err := openExistingFile(path)
		if err != nil {
			return Error{DurabilityUnavailable}
		}
		err = syncFile(f)
		if err == nil && name == "tracker.db" && s.lease != nil {
			physical, e := physicalIdentity(f)
			body, e2 := readOwner(s.lease)
			if e != nil || e2 != nil || string(body) != ownerIdentity(s.manifest.ID, s.instance, s.directory, physical) {
				err = Error{Corrupt}
			}
		}
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	if err := s.inject("directory_sync"); err != nil {
		return err
	}
	return syncDirectory(s.directory)
}

func (s *Store) verifyPhysicalBinding() error {
	if s.lease == nil {
		return nil
	} // Unpublished initialization staging only.
	canonical, err := privateDirectory(s.directory)
	if err != nil || canonical != s.directory {
		return Error{Corrupt}
	}
	f, err := openExistingFile(filepath.Join(s.directory, "tracker.db"))
	if err != nil {
		return err
	}
	physical, err := physicalIdentity(f)
	_ = f.Close()
	body, e := readOwner(s.lease)
	if err != nil || e != nil || string(body) != ownerIdentity(s.manifest.ID, s.instance, s.directory, physical) {
		return Error{Corrupt}
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.db != nil && s.db.Close() != nil {
		err = Error{DurabilityUnavailable}
	}
	if e := s.lease.close(); e != nil {
		err = e
	}
	return err
}
