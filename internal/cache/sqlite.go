package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db         *sql.DB
	useFTS     bool
	forceNoFTS bool
	cachePath  string
	lockPath   string
}

type LockHandle struct {
	file  *os.File
	path  string
	owner WriterOwner
}

type WriterOwner struct {
	Operation string    `json:"operation"`
	RepoID    string    `json:"repo_id,omitempty"`
	StartedAt time.Time `json:"started_at"`
	PID       int       `json:"pid"`
	CachePath string    `json:"cache_path"`
}

type WriterLease struct {
	lock  *LockHandle
	Owner WriterOwner
}

type WriterRequest struct {
	Operation string
	RepoID    string
	LockPath  string
}

func NewSQLiteStore(ctx context.Context, dataSourceName string) (*SQLiteStore, error) {
	return newSQLiteStore(ctx, dataSourceName, false)
}

func newSQLiteStore(ctx context.Context, dataSourceName string, forceNoFTS bool) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	cachePath := cachePathForDataSource(dataSourceName)
	lockPath := writerLockPath(cachePath)
	store := &SQLiteStore{db: db, forceNoFTS: forceNoFTS, cachePath: cachePath, lockPath: lockPath}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if dataSourceName != ":memory:" {
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	useFTS := !forceNoFTS && detectFTS5(ctx, db)
	store.useFTS = useFTS
	if dataSourceName == ":memory:" {
		if err := runMigrations(ctx, db, useFTS); err != nil {
			_ = db.Close()
			return nil, err
		}
		return store, nil
	}
	compat, err := CheckVersionCompatibility(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !compat.PermitWrites {
		_ = db.Close()
		return nil, &SchemaVersionError{Compat: compat}
	}
	lease, err := store.AcquireWriter(ctx, WriterRequest{Operation: "migration"})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := runMigrations(ctx, db, useFTS); err != nil {
		_ = store.ReleaseWriter(context.Background(), lease)
		_ = db.Close()
		return nil, err
	}
	if err := store.ReleaseWriter(context.Background(), lease); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func NewSQLiteReadOnlyStore(ctx context.Context, dataSourceName string) (*SQLiteStore, error) {
	if _, err := os.Stat(dataSourceName); err != nil {
		return nil, fmt.Errorf("cache: cannot open read-only store: %w", err)
	}
	dsn := dataSourceName + "?mode=ro&_journal_mode=WAL"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	compat, err := CheckVersionCompatibility(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !compat.PermitWrites {
		_ = db.Close()
		return nil, &SchemaVersionError{Compat: compat}
	}
	useFTS := detectFTS5(ctx, db)
	return &SQLiteStore{db: db, useFTS: useFTS, forceNoFTS: false, cachePath: dataSourceName, lockPath: ""}, nil
}

func (s *SQLiteStore) SchemaVersion(ctx context.Context) (int, error) {
	return schemaVersion(ctx, s.db)
}

func NewInMemorySQLiteStore(ctx context.Context) (*SQLiteStore, error) {
	return NewSQLiteStore(ctx, ":memory:")
}

func cachePathForDataSource(dataSourceName string) string {
	if dataSourceName == ":memory:" || strings.HasPrefix(dataSourceName, "file:") {
		return dataSourceName
	}
	if abs, err := filepath.Abs(dataSourceName); err == nil {
		return abs
	}
	return dataSourceName
}

func writerLockPath(cachePath string) string {
	if cachePath == "" || cachePath == ":memory:" || strings.HasPrefix(cachePath, "file:") {
		return filepath.Join(os.TempDir(), "gitcode-mcp-cache-writer.lock")
	}
	return cachePath + ".writer.lock"
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalJSON[T any](raw string) (T, error) {
	var v T
	if raw == "" {
		return v, nil
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, err
	}
	return v, nil
}

func execTx(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func txRollbackOnError(tx *sql.Tx, errp *error) {
	if *errp != nil {
		_ = tx.Rollback()
	}
}

func (s *SQLiteStore) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return ErrCacheCorruption{Path: "sqlite", Detail: result}
	}
	return nil
}

func (s *SQLiteStore) Checkpoint(ctx context.Context, reason string) error {
	_ = reason
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return ErrLockContention{Path: s.cachePath, HolderHint: "sqlite checkpoint busy"}
	}
	return nil
}

func notFoundErr(thing, id string) error {
	return fmt.Errorf("%w: %s %s", ErrNotFound, thing, id)
}
