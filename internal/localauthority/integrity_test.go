package localauthority

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func corruptExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal("synthetic corruption fixture failed")
	}
}

func alterProtected(t *testing.T, db *sql.DB, trigger, query string, args ...any) {
	t.Helper()
	corruptExec(t, db, "DROP TRIGGER "+trigger)
	corruptExec(t, db, query, args...)
	for _, statement := range strings.Split(schemaSQL, ";\n") {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TRIGGER "+trigger+" ") {
			corruptExec(t, db, strings.TrimSpace(statement))
			return
		}
	}
	t.Fatal("required fixture guard missing")
}

func TestLogicalAuthorityCorruptionRefusesInspectionAndWriter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*testing.T, *sql.DB, authority.MutationReceipt)
	}{
		{"missing_relation", func(t *testing.T, db *sql.DB, link authority.MutationReceipt) {
			corruptExec(t, db, "DELETE FROM relations WHERE link_id=?", link.Citation.EntityID)
		}},
		{"wrong_relation", func(t *testing.T, db *sql.DB, link authority.MutationReceipt) {
			corruptExec(t, db, "UPDATE relations SET relation='relates_to' WHERE link_id=?", link.Citation.EntityID)
		}},
		{"audit_operation", func(t *testing.T, db *sql.DB, link authority.MutationReceipt) {
			alterProtected(t, db, "immutable_audit_update", "UPDATE audit SET operation='issue.update' WHERE sequence=?", link.ProjectSequence)
		}},
		{"audit_uuid", func(t *testing.T, db *sql.DB, link authority.MutationReceipt) {
			alterProtected(t, db, "immutable_audit_update", "UPDATE audit SET operation_id=? WHERE sequence=?", testProject, link.ProjectSequence)
		}},
		{"audit_hash", func(t *testing.T, db *sql.DB, link authority.MutationReceipt) {
			alterProtected(t, db, "immutable_audit_update", "UPDATE audit SET actor_hash=? WHERE sequence=?", strings.Repeat("a", 64), link.ProjectSequence)
		}},
		{"audit_time", func(t *testing.T, db *sql.DB, link authority.MutationReceipt) {
			alterProtected(t, db, "immutable_audit_update", "UPDATE audit SET recorded_at='2026-01-01T00:00:00Z' WHERE sequence=?", link.ProjectSequence)
		}},
		{"audit_oversize", func(t *testing.T, db *sql.DB, link authority.MutationReceipt) {
			alterProtected(t, db, "immutable_audit_update", "UPDATE audit SET operation_id=? WHERE sequence=?", strings.Repeat("synthetic-private-audit", 8192), link.ProjectSequence)
		}},
		{"missing_guard", func(t *testing.T, db *sql.DB, _ authority.MutationReceipt) {
			corruptExec(t, db, "DROP TRIGGER immutable_revisions_update")
		}},
		{"changed_guard", func(t *testing.T, db *sql.DB, _ authority.MutationReceipt) {
			corruptExec(t, db, "DROP TRIGGER immutable_receipts_update")
			corruptExec(t, db, "CREATE TRIGGER immutable_receipts_update BEFORE UPDATE ON receipts BEGIN SELECT 1; END")
		}},
		{"unexpected_schema", func(t *testing.T, db *sql.DB, _ authority.MutationReceipt) {
			corruptExec(t, db, "CREATE TABLE synthetic_extra(value TEXT)")
		}},
		{"sequence_gap", func(t *testing.T, db *sql.DB, _ authority.MutationReceipt) {
			corruptExec(t, db, "PRAGMA foreign_keys=OFF")
			alterProtected(t, db, "immutable_revisions_update", "UPDATE revisions SET sequence=sequence+100")
			alterProtected(t, db, "immutable_receipts_update", "UPDATE receipts SET sequence=sequence+100,receipt=json_set(receipt,'$.project_sequence',sequence+100)")
			alterProtected(t, db, "immutable_audit_update", "UPDATE audit SET sequence=sequence+100")
			corruptExec(t, db, "PRAGMA foreign_keys=ON")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, registry, dir := testStore(t)
			issue := createIssue(t, s, "synthetic-integrity-issue")
			milestone := mustMutate(t, s, request(t, "milestone.create", "synthetic-integrity-milestone", authority.MilestonePayload{Title: "Synthetic milestone"}))
			link := mustMutate(t, s, request(t, "link.create", "synthetic-integrity-link", authority.LinkPayload{From: issue.Citation, To: milestone.Citation, Relation: "milestone"}))
			if s.Close() != nil {
				t.Fatal("fixture quiesce failed")
			}
			db, err := openDB(context.Background(), filepath.Join(dir, "tracker.db"), false)
			if err != nil {
				t.Fatal("synthetic corruption store unavailable")
			}
			tc.corrupt(t, db, link)
			if db.Close() != nil {
				t.Fatal("fixture corruption did not join")
			}
			for _, writer := range []bool{false, true} {
				var reopened *Store
				if writer {
					reopened, err = OpenWriter(context.Background(), registry, dir, testProject)
				} else {
					reopened, err = OpenReadOnly(context.Background(), dir, testProject)
				}
				if reopened != nil {
					_ = reopened.Close()
				}
				var typed Error
				if !errors.As(err, &typed) || typed.Code != Corrupt {
					t.Fatal("logically inconsistent authority admitted")
				}
			}
		})
	}
}

func TestAuditRejectsMalformedFieldsBeforeOutput(t *testing.T) {
	s, _, _ := testStore(t)
	r := createIssue(t, s, "synthetic-audit-bound")
	alterProtected(t, s.db, "immutable_audit_update", "UPDATE audit SET operation_id=? WHERE sequence=?", strings.Repeat("synthetic-private-audit", 8192), r.ProjectSequence)
	entries, err := s.Audit(context.Background(), 0, 100)
	var typed Error
	if len(entries) != 0 || !errors.As(err, &typed) || typed.Code != Corrupt {
		t.Fatal("invalid audit value escaped the typed bound")
	}
}
