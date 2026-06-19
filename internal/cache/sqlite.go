package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db         *sql.DB
	useFTS     bool
	forceNoFTS bool
}

type LockHandle struct {
	file *os.File
	path string
}

func NewSQLiteStore(ctx context.Context, dataSourceName string) (*SQLiteStore, error) {
	return newSQLiteStore(ctx, dataSourceName, false)
}

func newSQLiteStore(ctx context.Context, dataSourceName string, forceNoFTS bool) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	useFTS := !forceNoFTS && detectFTS5(ctx, db)
	if err := runMigrations(ctx, db, useFTS); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db, useFTS: useFTS, forceNoFTS: forceNoFTS}, nil
}

func NewInMemorySQLiteStore(ctx context.Context) (*SQLiteStore, error) {
	return NewSQLiteStore(ctx, ":memory:")
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

func notFoundErr(thing, id string) error {
	return fmt.Errorf("%w: %s %s", ErrNotFound, thing, id)
}
