//go:build !windows

package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

// This is a bounded malformed-peer fixture, not an authoritative server. OS
// peer authentication is real; response contents are intentionally untrusted.
func recoveryPeerFixture(t *testing.T, caps authority.Negotiation, result any, peerError *RPCError) (*RPCClient, <-chan struct{}) {
	t.Helper()
	dir, err := shortWorkspaceTemp(t, "rp-")
	if err != nil {
		t.Fatal("private recovery peer directory unavailable")
	}
	path := filepath.Join(dir, "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil || os.Chmod(path, 0600) != nil {
		t.Fatal("private recovery peer socket unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	stopListener := context.AfterFunc(ctx, func() { _ = l.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		count := 2
		if result == nil && peerError == nil {
			count = 1 // incompatible negotiation must not send the operation
		}
		for i := 0; i < count; i++ {
			conn, err := l.AcceptUnix()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			var req RPCRequest
			if json.NewDecoder(io.LimitReader(conn, authority.MaxContractBytes)).Decode(&req) != nil {
				_ = conn.Close()
				return
			}
			response := RPCResponse{JSONRPC: jsonrpcVersion, ID: req.ID, Result: result, Error: peerError}
			if i == 0 {
				response.Result, response.Error = caps, nil
			}
			_ = json.NewEncoder(conn).Encode(response)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		cancel()
		stopListener()
		_ = l.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("private recovery peer did not join")
		}
	})
	return &RPCClient{Network: "unix", Address: path}, done
}

func TestAuthorityRecoveryOldInventoryStopsBeforeOperation(t *testing.T) {
	for _, change := range []string{"old", "unknown", "reordered"} {
		caps := authorityRuntimeCapabilities()
		caps.Methods = append([]string(nil), caps.Methods...)
		switch change {
		case "old":
			caps.Methods = caps.Methods[:5]
		case "unknown":
			caps.Methods = append(caps.Methods, "AuthorityV1.synthetic-private")
		case "reordered":
			caps.Methods[0], caps.Methods[1] = caps.Methods[1], caps.Methods[0]
		}
		client, done := recoveryPeerFixture(t, caps, nil, nil)
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.EnqueueRecovery", recoveryIPCRequests()["AuthorityV1.EnqueueRecovery"], nil), authority.Unsupported)
		<-done
		if client.nextID.Load() != 1 {
			t.Fatal("incompatible runtime received recovery work")
		}
	}
}

func TestAuthorityRecoveryForgedPeerResultsRejectedWhenDiscarded(t *testing.T) {
	for _, method := range []string{"AuthorityV1.EnqueueRecovery", "AuthorityV1.RecoveryJob", "AuthorityV1.RecoveryJobs", "AuthorityV1.CancelRecovery", "AuthorityV1.RetryRecovery"} {
		for _, tamper := range []string{"project", "job", "raw-selector"} {
			v := recoveryExampleView()
			if tamper == "project" {
				v.ProjectID = "44444444-4444-4444-8444-444444444444"
			}
			if tamper == "job" {
				v.JobID = "44444444-4444-4444-8444-444444444444"
			}
			var bad any = v
			if method == "AuthorityV1.EnqueueRecovery" {
				bad = localauthority.RecoveryJobAdmission{Schema: 1, ProjectID: v.ProjectID, OperationID: v.JobID, Outcome: "queued", Job: &v}
			}
			if method == "AuthorityV1.RecoveryJobs" {
				bad = localauthority.RecoveryJobsPage{Schema: 1, ProjectID: v.ProjectID, Generation: 2, SnapshotDigest: strings.Repeat("a", 64), Retained: 1, Jobs: []localauthority.RecoveryJobView{v}}
				if tamper == "job" {
					// A list has no single-job selector; use a structurally
					// unrelated continuation instead of claiming that it does.
					p := bad.(localauthority.RecoveryJobsPage)
					p.NextCursor = "synthetic-private-peer-cursor"
					bad = p
				}
			}
			if tamper == "raw-selector" {
				body, _ := json.Marshal(bad)
				var shape map[string]any
				_ = json.Unmarshal(body, &shape)
				shape["path"] = "synthetic-private-peer-coordinate"
				bad = shape
			}
			client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), bad, nil)
			requireAuthorityCode(t, client.Call(context.Background(), method, recoveryIPCRequests()[method], nil), authority.InvalidContract)
			<-done
		}
	}
}

func TestAuthorityRecoveryPeerErrorsAreFiniteAndPrivate(t *testing.T) {
	for _, code := range []localauthority.Code{localauthority.JobsCorrupt, localauthority.JobsUncertain, localauthority.JobNotFound, localauthority.JobRetryLimit, "synthetic-private-unknown"} {
		peerError := &RPCError{Code: -32000, DiagnosticCode: string(code), Message: "synthetic-private-peer-body"}
		client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), nil, peerError)
		err := client.Call(context.Background(), "AuthorityV1.RecoveryJob", recoveryIPCRequests()["AuthorityV1.RecoveryJob"], nil)
		<-done
		if code == "synthetic-private-unknown" {
			requireAuthorityCode(t, err, authority.Unsupported)
			continue
		}
		var fixed localauthority.Error
		if !errors.As(err, &fixed) || fixed.Code != code || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatal("raw peer message replaced fixed recovery error")
		}
	}
}

func TestAuthorityRecoveryFalseActionAcknowledgementDiscarded(t *testing.T) {
	for _, method := range []string{"AuthorityV1.CancelRecovery", "AuthorityV1.RetryRecovery"} {
		for _, state := range []string{"queued", "running", "failed"} {
			v := recoveryExampleView()
			v.State = state
			if state == "running" {
				v.Attempts = 1
				v.Generation = 2
			}
			if state == "failed" {
				v.Code = "operation_failed"
				v.Attempts, v.Generation = 1, 2
			}
			if method == "AuthorityV1.CancelRecovery" && state == "failed" {
				continue // canonical inactive cancellation is an honest no-op
			}
			client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), v, nil)
			req := recoveryIPCRequests()[method].(localauthority.RecoveryJobAction)
			if method == "AuthorityV1.RetryRecovery" {
				req.ExpectedGeneration = v.Generation
			}
			requireAuthorityCode(t, client.Call(context.Background(), method, req, nil), authority.InvalidContract)
			<-done
		}
	}
	view := recoveryExampleView()
	bad := localauthority.RecoveryJobAdmission{Schema: 1, ProjectID: view.ProjectID, OperationID: view.JobID, Outcome: "queued", Job: &view}
	req := recoveryIPCRequests()["AuthorityV1.EnqueueRecovery"].(localauthority.RecoveryJobRequest)
	req.ObservedGeneration = 3
	client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), bad, nil)
	requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.EnqueueRecovery", req, nil), authority.InvalidContract)
	<-done
}

func TestAuthorityRecoveryPageCannotLoseDeclaredSnapshotRows(t *testing.T) {
	for _, badCase := range []string{"fresh-empty", "fresh-prefix", "resumed-digest", "resumed-total"} {
		v := recoveryExampleView()
		p := localauthority.RecoveryJobsPage{Schema: 1, ProjectID: v.ProjectID, Generation: 4, SnapshotDigest: strings.Repeat("a", 64), Retained: 2, Jobs: []localauthority.RecoveryJobView{v}}
		req := localauthority.RecoveryJobsRequest{ProjectID: v.ProjectID, Limit: 100}
		switch badCase {
		case "fresh-empty":
			p.Jobs = []localauthority.RecoveryJobView{}
		case "resumed-digest":
			p.Offset = 1
			req.Cursor = recoveryWireCursor(v.ProjectID, 4, strings.Repeat("b", 64), "11111111-1111-4111-8111-111111111111", 1, 2)
		case "resumed-total":
			p.Offset = 1
			req.Cursor = recoveryWireCursor(v.ProjectID, 4, strings.Repeat("a", 64), "11111111-1111-4111-8111-111111111111", 1, 3)
		}
		client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), p, nil)
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.RecoveryJobs", req, nil), authority.InvalidContract)
		<-done
	}
}

func TestAuthorityRecoveryPageRejectsImpossibleSnapshotGeneration(t *testing.T) {
	for _, badCase := range []string{"initial-nonempty", "snapshot-behind-job", "too-many-retained"} {
		v := recoveryExampleView()
		p := localauthority.RecoveryJobsPage{Schema: 1, ProjectID: v.ProjectID, Generation: 2, SnapshotDigest: strings.Repeat("a", 64), Retained: 1, Jobs: []localauthority.RecoveryJobView{v}}
		switch badCase {
		case "initial-nonempty":
			p.Generation = 1
		case "snapshot-behind-job":
			p.Jobs[0].State, p.Jobs[0].Generation, p.Jobs[0].Attempts, p.Jobs[0].Code = "failed", 3, 1, "operation_failed"
		case "too-many-retained":
			p.Retained = 2
			p.NextCursor = recoveryWireCursor(p.ProjectID, p.Generation, p.SnapshotDigest, v.JobID, 1, 2)
		}
		client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), p, nil)
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.RecoveryJobs", localauthority.RecoveryJobsRequest{ProjectID: v.ProjectID, Limit: 100}, nil), authority.InvalidContract)
		<-done
	}
}

func TestAuthorityRecoveryPageRejectsMultipleActiveJobs(t *testing.T) {
	for _, states := range [][2]string{{"queued", "queued"}, {"running", "cancelling"}} {
		jobs := []localauthority.RecoveryJobView{recoveryExampleView(), recoveryExampleView()}
		jobs[1].JobID = "44444444-4444-4444-8444-444444444444"
		for i := range jobs {
			jobs[i].State = states[i]
			if states[i] != "queued" {
				jobs[i].Generation, jobs[i].Attempts = 2, 1
			}
			if jobs[i].Validate() != nil {
				t.Fatal("multiple-active control is not otherwise a valid job view")
			}
		}
		p := localauthority.RecoveryJobsPage{Schema: 1, ProjectID: authorityExampleProject, Generation: 5, SnapshotDigest: strings.Repeat("a", 64), Retained: 2, Jobs: jobs}
		client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), p, nil)
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.RecoveryJobs", localauthority.RecoveryJobsRequest{ProjectID: p.ProjectID, Limit: 100}, nil), authority.InvalidContract)
		<-done
	}
}

func TestAuthorityRecoveryImpossibleViewHistoryRejected(t *testing.T) {
	for _, badCase := range []string{"running-first-generation", "generation-below-attempts", "failed-without-attempt"} {
		v := recoveryExampleView()
		v.State, v.Generation, v.Attempts = "running", 2, 1
		switch badCase {
		case "running-first-generation":
			v.Generation = 1
		case "generation-below-attempts":
			v.Attempts = 8
		case "failed-without-attempt":
			v.State, v.Attempts, v.Code = "failed", 0, "operation_failed"
		}
		client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), v, nil)
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.RecoveryJob", recoveryIPCRequests()["AuthorityV1.RecoveryJob"], nil), authority.InvalidContract)
		<-done
	}
}
