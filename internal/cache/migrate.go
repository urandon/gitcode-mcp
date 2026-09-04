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
	FromVersion       int
	ToVersion         int
	Applied           []int
	BackupPath        string
	BackupVerified    bool
	IdentityPreserved bool
	Compatibility     VersionCompatibility
}

func InspectCacheMigration(ctx context.Context, dataSourceName string) (*MigrateCacheResult, error) {
	if _, err := os.Stat(dataSourceName); err != nil {
		if os.IsNotExist(err) {
			return &MigrateCacheResult{FromVersion: 0, ToVersion: currentSchemaVersion}, nil
		}
		return nil, fmt.Errorf("cache: cannot access cache file: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(dataSourceName))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	compat, err := CheckVersionCompatibility(ctx, db)
	if err != nil {
		return nil, err
	}
	return &MigrateCacheResult{FromVersion: compat.DetectedVersion, ToVersion: currentSchemaVersion, Compatibility: compat}, nil
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
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return nil, fmt.Errorf("cache: failed to checkpoint WAL before migration: %w", err)
	}

	backupPath, err := backupCache(dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("cache: failed to create backup before migration: %w", err)
	}
	backupVersion, backupIdentity, err := verifyCacheBackup(ctx, backupPath)
	if err != nil {
		_ = os.Remove(backupPath)
		return nil, fmt.Errorf("cache: failed to verify backup before migration: %w", err)
	}
	if backupVersion != beforeVersion {
		_ = os.Remove(backupPath)
		return nil, fmt.Errorf("cache: backup schema version %d does not match source version %d", backupVersion, beforeVersion)
	}

	useFTS := !forceNoFTS && detectFTS5(ctx, db)

	applied := make([]int, 0)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, m := range migrations {
		if m.version <= beforeVersion {
			continue
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
		applied = append(applied, m.version)
	}
	identityPreserved, err := verifyMigrationTransaction(ctx, tx, backupIdentity)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}

	return &MigrateCacheResult{
		FromVersion:       beforeVersion,
		ToVersion:         currentSchemaVersion,
		Applied:           applied,
		BackupPath:        backupPath,
		BackupVerified:    true,
		IdentityPreserved: identityPreserved,
		Compatibility:     compat,
	}, nil
}

func verifyCacheBackup(ctx context.Context, path string) (int, string, error) {
	db, err := sql.Open("sqlite", sqliteImmutableReadOnlyDSN(path))
	if err != nil {
		return 0, "", err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return 0, "", err
	}
	if integrity != "ok" {
		return 0, "", fmt.Errorf("integrity_check returned %q", integrity)
	}
	version, err := schemaVersion(ctx, db)
	if err != nil {
		return 0, "", err
	}
	var identity string
	if version >= 17 {
		if err := db.QueryRowContext(ctx, `SELECT cache_uuid FROM cache_identity WHERE identity_key = 1`).Scan(&identity); err != nil {
			return 0, "", err
		}
	}
	return version, identity, nil
}

func verifyMigrationTransaction(ctx context.Context, tx *sql.Tx, expectedIdentity string) (bool, error) {
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version); err != nil {
		return false, err
	}
	if version != currentSchemaVersion {
		return false, fmt.Errorf("cache: migration verification found schema version %d, expected %d", version, currentSchemaVersion)
	}
	var integrity string
	if err := tx.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return false, err
	}
	if integrity != "ok" {
		return false, fmt.Errorf("cache: migration integrity_check returned %q", integrity)
	}
	var identity string
	if err := tx.QueryRowContext(ctx, `SELECT cache_uuid FROM cache_identity WHERE identity_key = 1`).Scan(&identity); err != nil {
		return false, err
	}
	if expectedIdentity != "" && identity != expectedIdentity {
		return false, fmt.Errorf("cache: migration changed cache identity")
	}
	return expectedIdentity == "" || identity == expectedIdentity, nil
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
