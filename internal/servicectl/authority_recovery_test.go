package servicectl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

const recoveryExampleJob authority.UUID = "22222222-2222-4222-8222-222222222222"

func TestAuthorityResultReplacementIsAtomicSnapshot(t *testing.T) {
	v := recoveryExampleView()
	v.State, v.Generation, v.Code = "cancelled", 2, "cancelled"
	queued := recoveryExampleView()
	queued.Generation = 3
	body, _ := json.Marshal(queued)
	if replaceAuthorityResult(body, &v) != nil || !reflect.DeepEqual(v, queued) {
		t.Fatal("complete response retained an omitted prior terminal code")
	}
	before := v
	requireAuthorityCode(t, replaceAuthorityResult([]byte(`{"schema":1,"code":"synthetic-private-peer"}`), &v), authority.InvalidContract)
	if !reflect.DeepEqual(before, v) {
		t.Fatal("invalid peer response partially replaced caller state")
	}
	page := localauthority.RecoveryJobsPage{Schema: 1, ProjectID: authorityExampleProject, Generation: 2, SnapshotDigest: strings.Repeat("a", 64), Retained: 0, Jobs: []localauthority.RecoveryJobView{}, NextCursor: "synthetic-private-prior-cursor"}
	clean := page
	clean.NextCursor = ""
	body, _ = json.Marshal(clean)
	if replaceAuthorityResult(body, &page) != nil || page.NextCursor != "" {
		t.Fatal("complete page response retained prior continuation")
	}
	for _, dst := range []any{nil, (*localauthority.RecoveryJobView)(nil), queued} {
		requireAuthorityCode(t, replaceAuthorityResult(body, dst), authority.InvalidContract)
	}
}

func recoveryExampleView() localauthority.RecoveryJobView {
	return localauthority.RecoveryJobView{Schema: 1, ProjectID: authorityExampleProject, JobID: recoveryExampleJob, Type: "document_recovery", State: "queued", Generation: 1, ObservedJournalGeneration: 2, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
}

func recoveryExampleReceipt() authority.MutationReceipt {
	return authority.MutationReceipt{ProjectID: authorityExampleProject, OperationID: recoveryExampleJob, Citation: authority.Citation{ProjectID: authorityExampleProject, Kind: authority.Document, EntityID: "33333333-3333-4333-8333-333333333333", Revision: 1}, ProjectSequence: 1, IdempotencyKeyHash: strings.Repeat("a", 64), IntentHash: strings.Repeat("b", 64), Evidence: "local_commit_confirmed", ProjectionState: "pending"}
}

func recoveryIPCRequests() map[string]any {
	return map[string]any{
		"AuthorityV1.EnqueueRecovery": localauthority.RecoveryJobRequest{ProjectID: authorityExampleProject, OperationID: recoveryExampleJob, ObservedGeneration: 2, WriteMode: "local"},
		"AuthorityV1.RecoveryJob":     localauthority.RecoveryJobLookup{ProjectID: authorityExampleProject, JobID: recoveryExampleJob},
		"AuthorityV1.RecoveryJobs":    localauthority.RecoveryJobsRequest{ProjectID: authorityExampleProject, Limit: 1},
		"AuthorityV1.CancelRecovery":  localauthority.RecoveryJobAction{ProjectID: authorityExampleProject, JobID: recoveryExampleJob, ExpectedGeneration: 1, WriteMode: "local"},
		"AuthorityV1.RetryRecovery":   localauthority.RecoveryJobAction{ProjectID: authorityExampleProject, JobID: recoveryExampleJob, ExpectedGeneration: 1, WriteMode: "local"},
	}
}

func TestAuthorityRecoveryParamsDenyBeforeTransportAndRemoteDispatch(t *testing.T) {
	for method, req := range recoveryIPCRequests() {
		t.Run(method, func(t *testing.T) {
			body, _ := json.Marshal(req)
			if _, err := authorityParams(method, body); err != nil {
				t.Fatal("valid shared recovery request was rejected")
			}
			for _, forbidden := range []string{"path", "markdown", "idempotency_key", "git_oid", "destination", "actor", "credential", "repo_id"} {
				var shape map[string]any
				_ = json.Unmarshal(body, &shape)
				shape[forbidden] = "synthetic-private-selector"
				c := RPCClient{Network: "mem", Address: "missing-recovery-fixture"}
				requireAuthorityCode(t, c.Call(context.Background(), method, shape, nil), authority.InvalidContract)
				if c.nextID.Load() != 0 {
					t.Fatal("invalid recovery selector reached transport")
				}
			}
			var shape map[string]any
			_ = json.Unmarshal(body, &shape)
			if _, write := shape["write_mode"]; write {
				for _, mode := range []string{"", "live", "dry-run"} {
					shape["write_mode"] = mode
					c := RPCClient{Network: "mem", Address: "missing-recovery-fixture"}
					requireAuthorityCode(t, c.Call(context.Background(), method, shape, nil), authority.InvalidContract)
					if c.nextID.Load() != 0 {
						t.Fatal("nonlocal recovery intent entered transport")
					}
				}
			}
			duplicate := append([]byte(`{"project_id":"12345678-1234-4234-8234-123456789abc",`), body[1:]...)
			_, err := authorityParams(method, duplicate)
			requireAuthorityCode(t, err, authority.InvalidContract)
			c := RPCClient{Network: "tcp", Address: "synthetic-private.invalid:1"}
			requireAuthorityCode(t, c.Call(context.Background(), method, req, nil), authority.PolicyDenied)
			if c.nextID.Load() != 0 {
				t.Fatal("recovery control attempted TCP negotiation")
			}
			_, err = (RPCServer{}).dispatch(context.Background(), method, body)
			requireAuthorityCode(t, err, authority.NoAuthority)
		})
	}
}

func TestAuthorityRecoveryResultBindsOriginalSelectorAndShape(t *testing.T) {
	for method, req := range recoveryIPCRequests() {
		t.Run(method, func(t *testing.T) {
			view := recoveryExampleView()
			if method == "AuthorityV1.CancelRecovery" {
				view.State, view.Generation, view.Code = "cancelled", 2, "cancelled"
			}
			if method == "AuthorityV1.RetryRecovery" {
				view.Generation = 2
			}
			var valid any = view
			if method == "AuthorityV1.EnqueueRecovery" {
				valid = localauthority.RecoveryJobAdmission{Schema: 1, ProjectID: view.ProjectID, OperationID: view.JobID, Outcome: "queued", Job: &view}
			}
			if method == "AuthorityV1.RecoveryJobs" {
				valid = localauthority.RecoveryJobsPage{Schema: 1, ProjectID: view.ProjectID, Generation: 2, SnapshotDigest: strings.Repeat("a", 64), Retained: 1, Jobs: []localauthority.RecoveryJobView{view}}
			}
			body, _ := json.Marshal(valid)
			if _, err := authorityRuntimeResult(method, req, body); err != nil {
				t.Fatal("valid protected recovery response rejected")
			}
			var shape map[string]any
			_ = json.Unmarshal(body, &shape)
			shape["project_id"] = "44444444-4444-4444-8444-444444444444"
			bad, _ := json.Marshal(shape)
			_, err := authorityRuntimeResult(method, req, bad)
			requireAuthorityCode(t, err, authority.InvalidContract)
			for _, bad := range [][]byte{[]byte(`{}`), []byte(`null`), []byte(`{"synthetic-private-peer-body":true}`), append(body, body...)} {
				_, err = authorityRuntimeResult(method, req, bad)
				requireAuthorityCode(t, err, authority.InvalidContract)
			}
		})
	}
	for _, method := range []string{"AuthorityV1.RecoveryJob", "AuthorityV1.CancelRecovery", "AuthorityV1.RetryRecovery"} {
		view := recoveryExampleView()
		view.JobID = "44444444-4444-4444-8444-444444444444"
		body, _ := json.Marshal(view)
		_, err := authorityRuntimeResult(method, recoveryIPCRequests()[method], body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
	for _, method := range []string{"AuthorityV1.CancelRecovery", "AuthorityV1.RetryRecovery"} {
		view := recoveryExampleView()
		body, _ := json.Marshal(view)
		req := localauthority.RecoveryJobAction{ProjectID: view.ProjectID, JobID: view.JobID, ExpectedGeneration: 3, WriteMode: "local"}
		_, err := authorityRuntimeResult(method, req, body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
}

func TestAuthorityRecoveryActionsRequireActualActionOutcome(t *testing.T) {
	for _, state := range []string{"queued", "running"} {
		view := recoveryExampleView()
		view.State, view.Generation = state, 2
		if state == "running" {
			view.Attempts = 1
		}
		body, _ := json.Marshal(view)
		_, err := authorityRuntimeResult("AuthorityV1.CancelRecovery", recoveryIPCRequests()["AuthorityV1.CancelRecovery"], body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
	for _, state := range []string{"failed", "interrupted", "cancelled", "queued"} {
		view := recoveryExampleView()
		view.State = state
		if state != "queued" {
			view.Code = "operation_failed"
		}
		if state == "cancelled" {
			view.Code = "cancelled"
		}
		if state != "queued" {
			view.Generation = 2
		}
		if state == "failed" {
			view.Attempts = 1
		}
		body, _ := json.Marshal(view)
		req := recoveryIPCRequests()["AuthorityV1.RetryRecovery"].(localauthority.RecoveryJobAction)
		req.ExpectedGeneration = view.Generation
		_, err := authorityRuntimeResult("AuthorityV1.RetryRecovery", req, body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
}

func TestAuthorityRecoveryAdmissionProofAndJobAreDistinct(t *testing.T) {
	req := recoveryIPCRequests()["AuthorityV1.EnqueueRecovery"]
	receipt := recoveryExampleReceipt()
	receipt.Replayed = true
	proof := localauthority.RecoveryJobAdmission{Schema: 1, ProjectID: receipt.ProjectID, OperationID: receipt.OperationID, Outcome: "already_settled", Receipt: &receipt}
	body, _ := json.Marshal(proof)
	if _, err := authorityRuntimeResult("AuthorityV1.EnqueueRecovery", req, body); err != nil {
		t.Fatal("original proof-only receipt rejected")
	}
	for _, change := range []string{"not-replayed", "wrong-kind", "wrong-operation", "fake-queued", "running-is-not-queued"} {
		badReceipt := receipt
		bad := proof
		bad.Receipt = &badReceipt
		view := recoveryExampleView()
		switch change {
		case "not-replayed":
			badReceipt.Replayed = false
		case "wrong-kind":
			badReceipt.Citation.Kind = authority.Issue
		case "wrong-operation":
			badReceipt.OperationID = "44444444-4444-4444-8444-444444444444"
		case "fake-queued":
			bad.Outcome = "queued"
		case "running-is-not-queued":
			view.State, view.Generation, view.Attempts = "running", 2, 1
			bad.Outcome, bad.Receipt, bad.Job = "queued", nil, &view
		}
		body, _ := json.Marshal(bad)
		_, err := authorityRuntimeResult("AuthorityV1.EnqueueRecovery", req, body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
	view := recoveryExampleView()
	admitted := localauthority.RecoveryJobAdmission{Schema: 1, ProjectID: view.ProjectID, OperationID: view.JobID, Outcome: "queued", Job: &view}
	later := req.(localauthority.RecoveryJobRequest)
	later.ObservedGeneration = 3
	body, _ = json.Marshal(admitted)
	_, err := authorityRuntimeResult("AuthorityV1.EnqueueRecovery", later, body)
	requireAuthorityCode(t, err, authority.InvalidContract)
	admitted.Outcome = "retained"
	body, _ = json.Marshal(admitted)
	if _, err := authorityRuntimeResult("AuthorityV1.EnqueueRecovery", later, body); err != nil {
		t.Fatal("retained admission incorrectly upgraded its original observation")
	}
}

func recoveryWireCursor(project authority.UUID, generation uint64, digest string, last authority.UUID, seen ...int) string {
	count := 1
	total := 2
	if len(seen) > 0 {
		count = seen[0]
	}
	if len(seen) > 1 {
		total = seen[1]
	}
	body, _ := json.Marshal(map[string]any{"project_id": project, "generation": generation, "digest": digest, "last": last, "seen": count, "total": total})
	return base64.RawURLEncoding.EncodeToString(body)
}

func TestAuthorityRecoveryPageScopeBudgetAndContinuation(t *testing.T) {
	v := recoveryExampleView()
	p := localauthority.RecoveryJobsPage{Schema: 1, ProjectID: v.ProjectID, Generation: 3, SnapshotDigest: strings.Repeat("a", 64), Retained: 2, Jobs: []localauthority.RecoveryJobView{v}}
	p.NextCursor = recoveryWireCursor(v.ProjectID, p.Generation, strings.Repeat("a", 64), v.JobID)
	req := localauthority.RecoveryJobsRequest{ProjectID: v.ProjectID, Limit: 1}
	body, _ := json.Marshal(p)
	if _, err := authorityRuntimeResult("AuthorityV1.RecoveryJobs", req, body); err != nil {
		t.Fatal("bounded continuation response rejected")
	}
	for _, change := range []string{"empty-with-cursor", "retained-underflow", "cursor-project", "cursor-generation", "cursor-last", "cursor-raw", "over-limit", "duplicate-job"} {
		bad := p
		bad.Jobs = append([]localauthority.RecoveryJobView(nil), p.Jobs...)
		switch change {
		case "empty-with-cursor":
			bad.Jobs = []localauthority.RecoveryJobView{}
		case "retained-underflow":
			bad.Retained = 0
		case "cursor-project":
			bad.NextCursor = recoveryWireCursor("44444444-4444-4444-8444-444444444444", 3, strings.Repeat("a", 64), v.JobID)
		case "cursor-generation":
			bad.NextCursor = recoveryWireCursor(v.ProjectID, 4, strings.Repeat("a", 64), v.JobID)
		case "cursor-last":
			bad.NextCursor = recoveryWireCursor(v.ProjectID, 3, strings.Repeat("a", 64), "44444444-4444-4444-8444-444444444444")
		case "cursor-raw":
			bad.NextCursor = "synthetic-private-peer-cursor"
		case "over-limit", "duplicate-job":
			another := v
			if change == "over-limit" {
				another.JobID = "44444444-4444-4444-8444-444444444444"
				another.State, another.Generation, another.Code = "cancelled", 2, "cancelled"
			}
			bad.Jobs = append(bad.Jobs, another)
			bad.NextCursor = ""
		}
		body, _ = json.Marshal(bad)
		_, err := authorityRuntimeResult("AuthorityV1.RecoveryJobs", req, body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
	prior := recoveryWireCursor(v.ProjectID, 4, strings.Repeat("a", 64), "11111111-1111-4111-8111-111111111111", 1, 3)
	req.Cursor = prior
	p.Offset, p.Retained = 1, 3
	p.Generation = 4
	p.NextCursor = recoveryWireCursor(v.ProjectID, 4, strings.Repeat("a", 64), v.JobID, 2, 3)
	if localauthority.VerifyRecoveryJobsPage(req, p) != nil {
		t.Fatal("valid resumed page rejected")
	}
	for _, cursor := range []string{recoveryWireCursor(v.ProjectID, 4, strings.Repeat("a", 64), v.JobID, 1, 3), recoveryWireCursor(v.ProjectID, 5, strings.Repeat("a", 64), "11111111-1111-4111-8111-111111111111", 1, 3), recoveryWireCursor(v.ProjectID, 4, strings.Repeat("b", 64), "11111111-1111-4111-8111-111111111111", 1, 3)} {
		req.Cursor = cursor
		requireAuthorityCode(t, localauthority.VerifyRecoveryJobsPage(req, p), authority.InvalidContract)
	}
	// Result-count bounds do not replace an independent actual wire-byte cap.
	p = localauthority.RecoveryJobsPage{Schema: 1, ProjectID: v.ProjectID, Generation: 200, SnapshotDigest: strings.Repeat("a", 64), Retained: 100, Jobs: []localauthority.RecoveryJobView{}}
	for i := 1; i <= 100; i++ {
		job := v
		job.JobID = authority.UUID(fmt.Sprintf("%08x-2222-4222-8222-222222222222", i))
		job.State, job.Generation, job.Attempts = "succeeded", 3, 1
		r := recoveryExampleReceipt()
		r.OperationID = job.JobID
		job.Receipt = &r
		p.Jobs = append(p.Jobs, job)
	}
	body, _ = json.Marshal(p)
	if len(body) <= localauthority.MaxRecoveryJobPageBytes || len(body) > authority.MaxProtectedResultBytes {
		t.Fatal("independent page budget fixture is not calibrated")
	}
	_, err := authorityRuntimeResult("AuthorityV1.RecoveryJobs", localauthority.RecoveryJobsRequest{ProjectID: v.ProjectID, Limit: 100}, body)
	requireAuthorityCode(t, err, authority.InvalidContract)
}
