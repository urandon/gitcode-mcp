package localauthority

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// Rebind every persisted digest to the changed synthetic payload so reference
// and graph tests cannot pass merely because fingerprint validation ran first.
func rewriteSyntheticPayload(t *testing.T, db *sql.DB, c authority.Citation, payload any, rebind bool) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal("synthetic payload encoding failed")
	}
	alterProtected(t, db, "immutable_revisions_update", "UPDATE revisions SET payload=?,payload_hash=? WHERE entity_id=? AND revision=?", string(data), hash(data), c.EntityID, c.Revision)
	if !rebind {
		return
	}
	var operation, actor string
	var seq uint64
	if db.QueryRow(`SELECT a.operation,r.actor_id,r.sequence FROM audit a JOIN revisions r ON a.sequence=r.sequence WHERE r.entity_id=? AND r.revision=?`, c.EntityID, c.Revision).Scan(&operation, &actor, &seq) != nil {
		t.Fatal("synthetic fingerprint input unavailable")
	}
	m := authority.MutationRequest{Operation: operation, Payload: data, Intent: authority.WriteIntent{ProjectID: c.ProjectID, WriteMode: "local", IdempotencyKey: "synthetic-integrity-validation"}}
	if c.Revision > 1 {
		m.Intent.EntityID, m.Intent.ExpectedRevision = c.EntityID, c.Revision-1
	}
	fingerprint := mutationFingerprint(m, actor)
	alterProtected(t, db, "immutable_receipts_update", "UPDATE receipts SET intent_hash=?,receipt=json_set(receipt,'$.intent_hash',?) WHERE sequence=?", fingerprint, fingerprint, seq)
	alterProtected(t, db, "immutable_audit_update", "UPDATE audit SET intent_hash=? WHERE sequence=?", fingerprint, seq)
}

func assertAuthorityAdmissionError(t *testing.T, registry *Registry, dir string, code Code) {
	t.Helper()
	for _, writer := range []bool{false, true} {
		var reopened *Store
		var err error
		if writer {
			reopened, err = OpenWriter(context.Background(), registry, dir, testProject)
		} else {
			reopened, err = OpenReadOnly(context.Background(), dir, testProject)
		}
		if reopened != nil {
			_ = reopened.Close()
		}
		var typed Error
		if !errors.As(err, &typed) || typed.Code != code {
			t.Fatal("invalid synthetic authority crossed its admission boundary")
		}
	}
}

func TestSemanticHistoryAndGraphCorruptionRefusesAdmission(t *testing.T) {
	for _, name := range []string{"changed_payload_digest", "noncanonical_payload", "opaque_intent_hash", "historical_dangling_link", "historical_future_revision", "historical_wrong_kind", "historical_comment_reparent", "active_blocks_cycle"} {
		t.Run(name, func(t *testing.T) {
			s, registry, dir := testStore(t)
			a := createIssue(t, s, "synthetic-a")
			b := createIssue(t, s, "synthetic-b")
			c := createIssue(t, s, "synthetic-c")
			firstLink := authority.LinkPayload{From: a.Citation, To: b.Citation, Relation: "blocks"}
			link := mustMutate(t, s, request(t, "link.create", "synthetic-link", firstLink))
			mustMutate(t, s, request(t, "link.update", "synthetic-link-edit", firstLink, link))
			other := mustMutate(t, s, request(t, "link.create", "synthetic-other-link", authority.LinkPayload{From: b.Citation, To: c.Citation, Relation: "blocks"}))
			comment := mustMutate(t, s, request(t, "comment.create", "synthetic-comment", authority.CommentPayload{Issue: a.Citation, Body: "Synthetic first"}))
			middle := mustMutate(t, s, request(t, "comment.update", "synthetic-comment-middle", authority.CommentPayload{Issue: a.Citation, Body: "Synthetic middle"}, comment))
			mustMutate(t, s, request(t, "comment.update", "synthetic-comment-current", authority.CommentPayload{Issue: a.Citation, Body: "Synthetic current"}, middle))
			future := mustMutate(t, s, request(t, "issue.update", "synthetic-future", authority.IssuePayload{Title: "Synthetic future", Body: "", State: "open"}, b))
			if s.Close() != nil {
				t.Fatal("synthetic history quiesce failed")
			}
			db, err := openDB(context.Background(), filepath.Join(dir, "tracker.db"), false)
			if err != nil {
				t.Fatal("synthetic history unavailable")
			}
			switch name {
			case "changed_payload_digest":
				rewriteSyntheticPayload(t, db, a.Citation, authority.IssuePayload{Title: "Changed synthetic", Body: "", State: "open"}, false)
			case "noncanonical_payload":
				var raw string
				if db.QueryRow("SELECT payload FROM revisions WHERE entity_id=? AND revision=1", a.Citation.EntityID).Scan(&raw) != nil {
					t.Fatal("synthetic canonical value unavailable")
				}
				// Typed JSON is valid but the persisted bytes are not canonical.
				alterProtected(t, db, "immutable_revisions_update", "UPDATE revisions SET payload=?,payload_hash=? WHERE entity_id=? AND revision=1", " "+raw, hash([]byte(" "+raw)), a.Citation.EntityID)
			case "opaque_intent_hash":
				fingerprint := strings.Repeat("a", 64)
				alterProtected(t, db, "immutable_receipts_update", "UPDATE receipts SET intent_hash=?,receipt=json_set(receipt,'$.intent_hash',?) WHERE sequence=?", fingerprint, fingerprint, a.ProjectSequence)
				alterProtected(t, db, "immutable_audit_update", "UPDATE audit SET intent_hash=? WHERE sequence=?", fingerprint, a.ProjectSequence)
			case "historical_dangling_link":
				missing, err := authority.NewUUID()
				if err != nil {
					t.Fatal("synthetic identity unavailable")
				}
				firstLink.To.EntityID = missing
				rewriteSyntheticPayload(t, db, link.Citation, firstLink, true)
			case "historical_future_revision":
				firstLink.To = future.Citation
				rewriteSyntheticPayload(t, db, link.Citation, firstLink, true)
			case "historical_wrong_kind":
				firstLink.Relation, firstLink.To.Kind = "relates_to", authority.Label
				rewriteSyntheticPayload(t, db, link.Citation, firstLink, true)
			case "historical_comment_reparent":
				rewriteSyntheticPayload(t, db, middle.Citation, authority.CommentPayload{Issue: b.Citation, Body: "Synthetic middle"}, true)
			case "active_blocks_cycle":
				rewriteSyntheticPayload(t, db, other.Citation, authority.LinkPayload{From: b.Citation, To: a.Citation, Relation: "blocks"}, true)
				corruptExec(t, db, "UPDATE relations SET to_id=?,to_revision=? WHERE link_id=?", a.Citation.EntityID, a.Citation.Revision, other.Citation.EntityID)
			}
			if db.Close() != nil {
				t.Fatal("synthetic history corruption did not join")
			}
			assertAuthorityAdmissionError(t, registry, dir, Corrupt)
		})
	}
}

// A bounded transaction builds the genuine 4096-edge boundary without thousands
// of physical publication barriers. Every tuple matches normal mutation output.
func seedSyntheticBlocks(t *testing.T, s *Store, from, to authority.Citation, count int) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal("synthetic graph transaction unavailable")
	}
	defer tx.Rollback()
	var sequence, alias int64
	if tx.QueryRow("SELECT sequence FROM project").Scan(&sequence) != nil || tx.QueryRow("SELECT next_number FROM aliases WHERE kind='link'").Scan(&alias) != nil {
		t.Fatal("synthetic graph counters unavailable")
	}
	for n := 0; n < count; n++ {
		m := request(t, "link.create", fmt.Sprintf("synthetic-block-%d", sequence), authority.LinkPayload{From: from, To: to, Relation: "blocks"})
		payload, err := canonicalPayload(m)
		if err != nil {
			t.Fatal("synthetic graph payload invalid")
		}
		m.Payload = payload
		id, err := authority.NewUUID()
		if err != nil {
			t.Fatal("synthetic graph identity unavailable")
		}
		op, err := authority.NewUUID()
		if err != nil {
			t.Fatal("synthetic graph operation unavailable")
		}
		sequence++
		keyHash := hash([]byte(string(testProject) + "\x00" + m.Intent.IdempotencyKey))
		fingerprint := mutationFingerprint(m, testActor.ID)
		at := "2026-01-01T00:00:00Z"
		r := authority.MutationReceipt{ProjectID: testProject, OperationID: op, Citation: authority.Citation{ProjectID: testProject, EntityID: id, Kind: authority.Link, Revision: 1}, ProjectSequence: uint64(sequence), IdempotencyKeyHash: keyHash, IntentHash: fingerprint, Evidence: "local_commit_confirmed", ProjectionState: "pending"}
		encoded, err := json.Marshal(r)
		if err != nil || r.Validate() != nil {
			t.Fatal("synthetic graph receipt invalid")
		}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{"INSERT INTO entities VALUES(?,?,?,1)", []any{id, authority.Link, alias}},
			{"INSERT INTO revisions VALUES(?,1,?,?,?,0,?,?)", []any{id, sequence, string(payload), hash(payload), testActor.ID, at}},
			{"INSERT INTO relations VALUES(?,?,?,?,?,'blocks')", []any{id, from.EntityID, from.Revision, to.EntityID, to.Revision}},
			{"INSERT INTO receipts VALUES(?,?,?,?,?)", []any{keyHash, fingerprint, op, sequence, string(encoded)}},
			{"INSERT INTO audit VALUES(?,?,?,1,'link.create',?,?,?,?)", []any{sequence, op, id, keyHash, fingerprint, hash([]byte(string(testProject) + "\x00" + testActor.ID)), at}},
		} {
			if _, err := tx.Exec(statement.query, statement.args...); err != nil {
				t.Fatal("synthetic graph tuple failed")
			}
		}
		alias++
	}
	if _, err := tx.Exec("UPDATE aliases SET next_number=? WHERE kind='link'", alias); err != nil {
		t.Fatal("synthetic graph alias failed")
	}
	if _, err := tx.Exec("UPDATE project SET sequence=?,generation=?", sequence, sequence+1); err != nil || tx.Commit() != nil {
		t.Fatal("synthetic graph publication failed")
	}
}

func TestActiveBlocksCapacityPreservesRestartQualification(t *testing.T) {
	s, registry, dir := testStore(t)
	a := createIssue(t, s, "synthetic-capacity-a")
	b := createIssue(t, s, "synthetic-capacity-b")
	seedSyntheticBlocks(t, s, a.Citation, b.Citation, maxActiveBlocks)
	_, err := s.Mutate(context.Background(), request(t, "link.create", "synthetic-over-cap", authority.LinkPayload{From: a.Citation, To: b.Citation, Relation: "blocks"}), testActor)
	var typed Error
	if !errors.As(err, &typed) || typed.Code != Capacity {
		t.Fatal("supported mutation exceeded restart-safe graph capacity")
	}
	if s.Close() != nil {
		t.Fatal("synthetic capacity quiesce failed")
	}
	reopened, err := OpenWriter(context.Background(), registry, dir, testProject)
	if err != nil {
		t.Fatalf("graph boundary lost restart qualification: %T", err)
	}
	// A coherently oversized external fixture is refused, never auto-trimmed.
	seedSyntheticBlocks(t, reopened, a.Citation, b.Citation, 1)
	if reopened.Close() != nil {
		t.Fatal("synthetic oversized graph did not join")
	}
	assertAuthorityAdmissionError(t, registry, dir, Capacity)
}
