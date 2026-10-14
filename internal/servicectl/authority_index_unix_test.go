//go:build !windows

package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

func TestAuthorityIndexAuthenticatedForgedResponsesRejectedWhenDiscarded(t *testing.T) {
	for method, req := range indexIPCRequests() {
		for _, tamper := range []string{"project", "selector", "raw", "false-action"} {
			if tamper == "false-action" && method != "AuthorityV1.CancelIndex" && method != "AuthorityV1.RetryIndex" {
				continue
			}
			body, _ := json.Marshal(indexExampleResult(method))
			var bad map[string]any
			_ = json.Unmarshal(body, &bad)
			switch tamper {
			case "project":
				bad["project_id"] = "55555555-5555-4555-8555-555555555555"
			case "selector":
				switch method {
				case "AuthorityV1.EnqueueIndex":
					bad["request_digest"] = strings.Repeat("b", 64)
				case "AuthorityV1.IndexJobs":
					bad["next_cursor"] = "synthetic-private-invalid-cursor"
				default:
					bad["job_id"] = "55555555-5555-4555-8555-555555555555"
				}
			case "raw":
				bad["path"] = "synthetic-private-peer-body"
			case "false-action":
				bad["state"], bad["code"], bad["generation"] = "queued", "", req.(localauthority.IndexJobAction).ExpectedGeneration
			}
			client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), bad, nil)
			requireAuthorityCode(t, client.Call(context.Background(), method, req, nil), authority.InvalidContract)
			<-done
		}
	}
}

func TestAuthorityIndexErrorsDoNotEchoPeerProse(t *testing.T) {
	for _, code := range []localauthority.Code{localauthority.IndexJobsCorrupt, localauthority.IndexJobsUncertain, localauthority.IndexJobNotFound, localauthority.ProjectionCorrupt, localauthority.ProjectionUncertain, localauthority.RepairCorrupt, localauthority.RepairUncertain, "synthetic-private-unknown"} {
		client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), nil, &RPCError{Code: -32000, DiagnosticCode: string(code), Message: "synthetic-private-peer-body"})
		err := client.Call(context.Background(), "AuthorityV1.IndexJob", indexIPCRequests()["AuthorityV1.IndexJob"], nil)
		<-done
		if code == "synthetic-private-unknown" {
			requireAuthorityCode(t, err, authority.Unsupported)
			continue
		}
		var typed localauthority.Error
		if !errors.As(err, &typed) || typed.Code != code || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatal("typed index failure reflected peer prose or lost identity")
		}
	}
}

func TestAuthorityIndexPriorInventoryNeverReceivesAdmission(t *testing.T) {
	for _, count := range []int{5, 10, 14} {
		caps := authorityRuntimeCapabilities()
		caps.Methods = append([]string{}, caps.Methods[:count]...)
		client, done := recoveryPeerFixture(t, caps, nil, nil)
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.EnqueueIndex", indexIPCRequests()["AuthorityV1.EnqueueIndex"], nil), authority.Unsupported)
		<-done
		if client.nextID.Load() != 1 {
			t.Fatal("old runtime inventory received index admission")
		}
	}
}

func TestAuthorityIndexAuthenticatedPeerCannotInventTerminalAtFirstStart(t *testing.T) {
	fresh := indexExampleView()
	fresh.Generation = 2
	reader, readDone := recoveryPeerFixture(t, authorityRuntimeCapabilities(), fresh, nil)
	requireAuthorityCode(t, reader.Call(context.Background(), "AuthorityV1.IndexJob", localauthority.IndexJobLookup{ProjectID: fresh.ProjectID, JobID: fresh.JobID}, nil), authority.InvalidContract)
	<-readDone
	for _, state := range []string{"succeeded", "cancelling", "failed", "interrupted", "cancelled", "queued"} {
		v := indexExampleView()
		v.State, v.Attempts, v.Generation = state, 1, 2
		if state == "succeeded" {
			v.Published = true
		} else if state == "failed" || state == "interrupted" {
			v.Code = "interrupted"
		} else if state == "cancelled" {
			v.Code = "cancelled"
			v.Generation = 3 // still missing joined cancellation transition
		}
		lookup := localauthority.IndexJobLookup{ProjectID: v.ProjectID, JobID: v.JobID}
		reader, readDone := recoveryPeerFixture(t, authorityRuntimeCapabilities(), v, nil)
		requireAuthorityCode(t, reader.Call(context.Background(), "AuthorityV1.IndexJob", lookup, nil), authority.InvalidContract)
		<-readDone
		req := localauthority.IndexJobAction{ProjectID: v.ProjectID, JobID: v.JobID, ExpectedGeneration: v.Generation, WriteMode: "local"}
		client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), v, nil)
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.RetryIndex", req, nil), authority.InvalidContract)
		<-done
	}
	// Genuine terminal confirmation needs start+publication. A verified repeat
	// of an already-succeeded job is safe and does not require another attempt.
	v := indexExampleView()
	v.State, v.Attempts, v.Generation, v.Published = "succeeded", 1, 3, true
	req := localauthority.IndexJobAction{ProjectID: v.ProjectID, JobID: v.JobID, ExpectedGeneration: 3, WriteMode: "local"}
	client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), v, nil)
	if client.Call(context.Background(), "AuthorityV1.RetryIndex", req, nil) != nil {
		t.Fatal("genuine terminal replay refused")
	}
	<-done
}
