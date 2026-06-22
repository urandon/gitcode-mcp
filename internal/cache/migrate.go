package cache

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type Confirmation struct {
	Confirmed bool
}

type MigrateCacheResult struct {
	FromVersion   int
	ToVersion     int
	Applied       []int
	BackupPath    string
	Compatibility VersionCompatibility
}

func MigrateCache(ctx context.Context, dataSourceName string, forceNoFTS bool) (*MigrateCacheResult, error) {
	return MigrateCacheWithConfirm(ctx, dataSourceName, forceNoFTS, Confirmation{Confirmed: true})
}

func MigrateCacheWithConfirm(ctx context.Context, dataSourceName string, forceNoFTS bool, confirm Confirmation) (*MigrateCacheResult, error) {
	if _, err := os.Stat(dataSourceName); err != nil {
		if os.IsNotExist(err) {
			return &MigrateCacheResult{FromVersion: 0, ToVersion: currentSchemaVersion, Applied: nil}, nil
		}
		return nil, fmt.Errorf("cache: cannot access cache file: %w", err)
	}

	db, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return nil, err
	}

	store := &SQLiteStore{db: db, forceNoFTS: forceNoFTS, cachePath: dataSourceName, lockPath: writerLockPath(dataSourceName)}

	lease, err := store.AcquireWriter(ctx, WriterRequest{Operation: "migration"})
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = store.ReleaseWriter(context.Background(), lease)
	}()

	compat, err := CheckVersionCompatibility(ctx, db)
	if err != nil {
		return nil, err
	}
	beforeVersion := compat.DetectedVersion

	if !compat.Compatible || beforeVersion <= 1 {
		return &MigrateCacheResult{FromVersion: beforeVersion, ToVersion: currentSchemaVersion, Applied: nil, Compatibility: compat}, nil
	}

	if beforeVersion == currentSchemaVersion {
		return &MigrateCacheResult{FromVersion: beforeVersion, ToVersion: currentSchemaVersion, Applied: nil, Compatibility: compat}, nil
	}

	if !confirm.Confirmed {
		return &MigrateCacheResult{
			FromVersion:   beforeVersion,
			ToVersion:     currentSchemaVersion,
			Applied:       nil,
			Compatibility: compat,
		}, nil
	}

	backupPath, err := backupCache(dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("cache: failed to create backup before migration: %w", err)
	}

	useFTS := !forceNoFTS && detectFTS5(ctx, db)

	applied := make([]int, 0)

	for _, m := range migrations {
		if m.version <= beforeVersion {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if err = m.apply(ctx, tx, useFTS); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, m.version); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		applied = append(applied, m.version)
	}

	return &MigrateCacheResult{
		FromVersion:   beforeVersion,
		ToVersion:     currentSchemaVersion,
		Applied:       applied,
		BackupPath:    backupPath,
		Compatibility: compat,
	}, nil
}

func backupCache(sourcePath string) (string, error) {
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	backupPath := sourcePath + ".backup-" + timestamp

	src, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("open source for backup: %w", err)
	}
	defer src.Close()

	dir := filepath.Dir(backupPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}

	dst, err := os.OpenFile(backupPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create backup file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(backupPath)
		return "", fmt.Errorf("copy backup: %w", err)
	}

	if err := dst.Sync(); err != nil {
		os.Remove(backupPath)
		return "", fmt.Errorf("sync backup: %w", err)
	}

	return backupPath, nil
}

func hasSchemaVersionTable(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'`).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func isEmptyDatabase(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type IN ('table', 'view', 'index', 'trigger') AND name NOT LIKE 'sqlite_%'`).Scan(&count)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}
