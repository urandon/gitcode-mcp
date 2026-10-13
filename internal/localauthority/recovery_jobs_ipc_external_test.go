//go:build darwin || linux

package localauthority_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/servicectl"
)

func recoveryIPCClient(t *testing.T, core *localauthority.Coordinator) *servicectl.RPCClient {
	t.Helper()
	server, err := servicectl.NewAuthorityServer(core)
	if err != nil {
		t.Fatal("recovery private server unavailable")
	}
	path := filepath.Join(privateTemp(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil || os.Chmod(path, 0600) != nil {
		t.Fatal("recovery private socket unavailable")
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, l) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Error("recovery private server did not stop cleanly")
			}
		case <-time.After(time.Second):
			t.Error("recovery private handlers did not join")
		}
	})
	return &servicectl.RPCClient{Network: "unix", Address: path}
}

func TestRecoveryIPCActualDurableCancelRetryAndOriginalReceipt(t *testing.T) {
	c, req, source := localauthority.NewRecoveryTransportFixture(t)
	client := recoveryIPCClient(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var page localauthority.RecoveryJobsPage
	list := localauthority.RecoveryJobsRequest{ProjectID: req.ProjectID, Limit: 100}
	if client.Call(ctx, "AuthorityV1.RecoveryJobs", list, &page) != nil || page.Retained != 0 || len(page.Jobs) != 0 || page.Validate() != nil {
		t.Fatal("empty protected job read admitted work or failed")
	}
	lookup := localauthority.RecoveryJobLookup{ProjectID: req.ProjectID, JobID: req.OperationID}
	var view localauthority.RecoveryJobView
	err := client.Call(ctx, "AuthorityV1.RecoveryJob", lookup, &view)
	var typed localauthority.Error
	if !errors.As(err, &typed) || typed.Code != localauthority.JobNotFound {
		t.Fatal("unknown recovery job lost typed error identity")
	}
	var admitted localauthority.RecoveryJobAdmission
	if client.Call(ctx, "AuthorityV1.EnqueueRecovery", req, &admitted) != nil || admitted.Validate() != nil || admitted.Outcome != "queued" {
		t.Fatal("private recovery admission failed")
	}
	if client.Call(ctx, "AuthorityV1.RecoveryJob", lookup, &view) != nil || !reflect.DeepEqual(view, *admitted.Job) {
		t.Fatal("protected get lost exact retained job")
	}
	action := localauthority.RecoveryJobAction{ProjectID: req.ProjectID, JobID: req.OperationID, ExpectedGeneration: view.Generation, WriteMode: "local"}
	if client.Call(ctx, "AuthorityV1.CancelRecovery", action, &view) != nil || view.State != "cancelled" || view.Attempts != 0 {
		t.Fatal("private queued cancellation failed")
	}
	var replay localauthority.RecoveryJobAdmission
	if client.Call(ctx, "AuthorityV1.EnqueueRecovery", req, &replay) != nil || replay.Outcome != "retained" || !reflect.DeepEqual(view, *replay.Job) {
		t.Fatal("admission replay resumed cancelled source work")
	}
	if client.Call(ctx, "AuthorityV1.RecoveryJobs", list, &page) != nil || page.Retained != 1 || len(page.Jobs) != 1 || page.Jobs[0].State != "cancelled" {
		t.Fatal("read page lost durable cancelled job")
	}
	action.ExpectedGeneration = view.Generation
	if client.Call(ctx, "AuthorityV1.RetryRecovery", action, &view) != nil || view.State != "queued" || view.Attempts != 0 {
		t.Fatal("private explicit retry did not retain original selector")
	}
	var retryReplay localauthority.RecoveryJobView
	if client.Call(ctx, "AuthorityV1.RetryRecovery", action, &retryReplay) != nil || !reflect.DeepEqual(view, retryReplay) {
		t.Fatal("generation-CAS retry replay duplicated work")
	}
	if localauthority.StartRecoveryTransportFixtureWorkers(c) != nil {
		t.Fatal("real recovery worker activation failed")
	}
	for {
		err := client.Call(ctx, "AuthorityV1.RecoveryJob", lookup, &view)
		var conflict authority.Error
		if err != nil && (!errors.As(err, &conflict) || conflict.Code != authority.Conflict) {
			t.Fatal("actual private job observation failed")
		}
		if err == nil && view.State == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("actual recovery worker did not settle bounded job")
		case <-time.After(time.Millisecond):
		}
	}
	if view.Validate() != nil || view.Attempts != 1 || view.Receipt == nil || view.Reconciled || view.Receipt.OperationID != req.OperationID || view.Receipt.Citation.EntityID != source.Intent.EntityID {
		t.Fatal("private recovery fabricated execution or original receipt")
	}
	var original authority.DocumentMutationResult
	if client.Call(ctx, "AuthorityV1.MutateDocument", source, &original) != nil || !original.Receipt.Replayed {
		t.Fatal("private original-source replay did not confirm identity")
	}
	original.Receipt.Replayed = false
	if !reflect.DeepEqual(*view.Receipt, original.Receipt) {
		t.Fatal("recovery receipt changed original source provenance")
	}
	var retained localauthority.RecoveryJobAdmission
	if client.Call(ctx, "AuthorityV1.EnqueueRecovery", req, &retained) != nil || retained.Outcome != "retained" || !reflect.DeepEqual(view, *retained.Job) {
		t.Fatal("settled recovery replay changed receipt/history")
	}
}

func TestRecoveryIPCUnknownProjectAndInvalidWritesDoNotCrossAuthority(t *testing.T) {
	c, req, _ := localauthority.NewRecoveryTransportFixture(t)
	client := recoveryIPCClient(t, c)
	ctx := context.Background()
	bad := req
	bad.ProjectID = "55555555-5555-4555-8555-555555555555"
	var typed authority.Error
	err := client.Call(ctx, "AuthorityV1.EnqueueRecovery", bad, nil)
	if !errors.As(err, &typed) || typed.Code != authority.NoAuthority {
		t.Fatal("unknown project recovered another local authority")
	}
	bad = req
	bad.WriteMode = "live"
	err = client.Call(ctx, "AuthorityV1.EnqueueRecovery", bad, nil)
	if !errors.As(err, &typed) || typed.Code != authority.InvalidContract {
		t.Fatal("remote write mode was admitted as local recovery")
	}
	var page localauthority.RecoveryJobsPage
	if client.Call(ctx, "AuthorityV1.RecoveryJobs", localauthority.RecoveryJobsRequest{ProjectID: req.ProjectID, Limit: 100}, &page) != nil || page.Retained != 0 {
		t.Fatal("rejected admission changed durable job state")
	}
	if strings.Contains(err.Error(), string(req.ProjectID)) {
		t.Fatal("typed recovery refusal included protected coordinates")
	}
}
