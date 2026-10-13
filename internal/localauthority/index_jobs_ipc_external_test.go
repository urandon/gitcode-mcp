//go:build darwin || linux

package localauthority_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

func TestIndexIPCActualDurableCancellationOwnerReopenAndRetry(t *testing.T) {
	c, req, reopen := localauthority.NewIndexTransportFixture(t)
	client := recoveryIPCClient(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list := localauthority.IndexJobsRequest{ProjectID: req.ProjectID, Limit: 32}
	var page localauthority.IndexJobsPage
	if client.Call(ctx, "AuthorityV1.IndexJobs", list, &page) != nil || page.State != "missing" || page.Retained != 0 {
		t.Fatal("actual index read did not preserve missing state")
	}
	var admitted localauthority.IndexJobAdmission
	if client.Call(ctx, "AuthorityV1.EnqueueIndex", req, &admitted) != nil || localauthority.VerifyIndexJobAdmission(req, admitted) != nil {
		t.Fatal("actual private index admission failed")
	}
	lookup := localauthority.IndexJobLookup{ProjectID: req.ProjectID, JobID: admitted.Job.JobID}
	var v localauthority.IndexJobView
	if client.Call(ctx, "AuthorityV1.IndexJob", lookup, &v) != nil || !reflect.DeepEqual(v, admitted.Job) {
		t.Fatal("private index lookup changed retained selector")
	}
	action := localauthority.IndexJobAction{ProjectID: req.ProjectID, JobID: v.JobID, ExpectedGeneration: v.Generation, WriteMode: "local"}
	if client.Call(ctx, "AuthorityV1.CancelIndex", action, &v) != nil || v.State != "cancelled" || v.Attempts != 0 {
		t.Fatal("private queued index cancellation failed")
	}
	if c.Close(ctx) != nil {
		t.Fatal("original index owner failed to join")
	}
	next := reopen()
	client = recoveryIPCClient(t, next)
	var retained localauthority.IndexJobAdmission
	if client.Call(ctx, "AuthorityV1.EnqueueIndex", req, &retained) != nil || !reflect.DeepEqual(v, retained.Job) || retained.RequestDigest != admitted.RequestDigest {
		t.Fatal("owner reopen lost original key or cancellation")
	}
	if client.Call(ctx, "AuthorityV1.CancelIndex", action, &v) != nil || !reflect.DeepEqual(v, retained.Job) {
		t.Fatal("original generation cancellation replay changed state")
	}
	bad := req
	bad.Sequence++
	err := client.Call(ctx, "AuthorityV1.EnqueueIndex", bad, nil)
	var domain authority.Error
	if !errors.As(err, &domain) || domain.Code != authority.Conflict {
		t.Fatal("reopen accepted retargeted same-key index intent")
	}
	action.ExpectedGeneration = v.Generation
	if client.Call(ctx, "AuthorityV1.RetryIndex", action, &v) != nil || v.State != "queued" || v.Attempts != 0 {
		t.Fatal("explicit private index retry failed")
	}
	var replay localauthority.IndexJobView
	if client.Call(ctx, "AuthorityV1.RetryIndex", action, &replay) != nil || !reflect.DeepEqual(v, replay) {
		t.Fatal("generation-fenced retry replay duplicated work")
	}
	if localauthority.StartRecoveryTransportFixtureWorkers(next) != nil {
		t.Fatal("joined index worker activation failed")
	}
	for {
		err := client.Call(ctx, "AuthorityV1.IndexJob", lookup, &v)
		if err != nil && (!errors.As(err, &domain) || domain.Code != authority.Conflict) {
			t.Fatal("actual private index observation failed")
		}
		if err == nil && v.State == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("actual index retry did not settle")
		case <-time.After(time.Millisecond):
		}
	}
	if v.Attempts != 1 || !v.Published || v.Sequence != req.Sequence || v.CatalogGeneration != 1 {
		t.Fatal("index completion fabricated catalog provenance")
	}
	if client.Call(ctx, "AuthorityV1.EnqueueIndex", req, &retained) != nil || !reflect.DeepEqual(v, retained.Job) {
		t.Fatal("successful index admission replay changed execution")
	}
	// Catalog completion is not vector readiness. Strict policy continues to
	// return honest lexical fallback and the canonical source citation.
	var search localauthority.SearchResult
	if client.Call(ctx, "AuthorityV1.Search", localauthority.SearchRequest{ProjectID: req.ProjectID, Query: "canonical source sentinel", Mode: "hybrid", Limit: 8}, &search) != nil || search.EffectiveMode != "full_text" || search.FallbackReason != "policy_denied" || len(search.Hits) != 1 {
		t.Fatal("metadata completion fabricated semantic readiness")
	}
}

func TestNativeIndexProcessActualPublicationRestartReplay(t *testing.T) {
	p, _, dir := nativeLaunchFixture(t)
	process := nativeProcess(t, p, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var receipt authority.MutationReceipt
	if process.Client().Call(ctx, "AuthorityV1.Mutate", nativeMutation("synthetic-native-index-source", "Native catalog source"), &receipt) != nil {
		t.Fatal("native index source mutation failed")
	}
	req := localauthority.IndexJobRequest{ProjectID: fixtureProject, Sequence: receipt.ProjectSequence, IdempotencyKey: "synthetic-native-index-original", WriteMode: "local"}
	var a localauthority.IndexJobAdmission
	if process.Client().Call(ctx, "AuthorityV1.EnqueueIndex", req, &a) != nil || localauthority.VerifyIndexJobAdmission(req, a) != nil {
		t.Fatal("native index admission failed")
	}
	lookup := localauthority.IndexJobLookup{ProjectID: fixtureProject, JobID: a.Job.JobID}
	var v localauthority.IndexJobView
	for {
		err := process.Client().Call(ctx, "AuthorityV1.IndexJob", lookup, &v)
		var domain authority.Error
		if err != nil && (!errors.As(err, &domain) || domain.Code != authority.Conflict) {
			t.Fatal("native index lookup failed")
		}
		if err == nil && v.State == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("native index publication did not settle")
		case <-time.After(time.Millisecond):
		}
	}
	if v.Attempts != 1 || !v.Published || process.Close(ctx) != nil {
		t.Fatal("native index owner did not publish and join")
	}
	next := nativeProcess(t, p, dir)
	// Startup may legitimately hold the short metadata turn. Only conflict is
	// retryable for observation; no write is retried until qualification joins.
	for {
		err := next.Client().Call(ctx, "AuthorityV1.IndexJob", lookup, &a.Job)
		var domain authority.Error
		if err == nil {
			break
		}
		if !errors.As(err, &domain) || domain.Code != authority.Conflict {
			t.Fatal("native restart lost retained index metadata")
		}
		select {
		case <-ctx.Done():
			t.Fatal("native restart metadata did not qualify")
		case <-time.After(time.Millisecond):
		}
	}
	if !reflect.DeepEqual(v, a.Job) {
		t.Fatal("native restart changed confirmed index identity")
	}
	if next.Client().Call(ctx, "AuthorityV1.EnqueueIndex", req, &a) != nil || !reflect.DeepEqual(v, a.Job) {
		t.Fatal("native restart replay re-executed index work")
	}
}
