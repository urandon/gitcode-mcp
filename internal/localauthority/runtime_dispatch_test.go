package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func activeRuntime(t *testing.T, knowledge bool) *Runtime {
	t.Helper()
	r, registry, directory, control := runtimeFixture(t)
	if knowledge {
		store, err := OpenWriter(context.Background(), registry, directory, testProject)
		if err != nil {
			t.Fatal("knowledge setup fixture writer unavailable")
		}
		_, err = store.SetupKnowledge(context.Background(), testProject, "synthetic-runtime-setup")
		_ = store.Close()
		if err != nil {
			t.Fatal("explicit runtime fixture knowledge setup failed")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	admitted := make(chan struct{})
	go func() { done <- r.Serve(ctx, func(ctx context.Context, _ net.Conn) { close(admitted); <-ctx.Done() }) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("active runtime did not join")
		}
	})
	client := runtimeClient(t, ctx, control)
	t.Cleanup(func() { _ = client.Close() })
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("qualified runtime handler not ready")
	}
	return r
}

func dispatchFixture(t *testing.T, r *Runtime, method string, request any) any {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal("dispatch fixture request invalid")
	}
	result, err := r.Dispatch(context.Background(), method, body)
	if err != nil {
		t.Fatalf("dispatch fixture failed for %s: %T", method, err)
	}
	return result
}

func runtimeStore(t *testing.T, r *Runtime) *Store {
	t.Helper()
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.projects[testProject] == nil {
		t.Fatal("runtime project fixture unavailable")
	}
	return r.projects[testProject].store
}

func TestRuntimeDispatchAttributedWritesAndProtectedReads(t *testing.T) {
	r := activeRuntime(t, false)
	caps := dispatchFixture(t, r, "Negotiate", authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}).(authority.Negotiation)
	if caps.Validate() != nil || !caps.Supports("MutateAttributed") || caps.Supports("Mutate") || caps.Supports("Search") {
		t.Fatal("runtime advertised wrong capability set")
	}
	mutation := authority.AttributedMutationRequest{ActorID: "synthetic-agent", Request: request(t, "issue.create", "runtime-original-key", authority.IssuePayload{Title: "Synthetic runtime task", Body: "Evidence", State: "open"})}
	first := dispatchFixture(t, r, "MutateAttributed", mutation).(authority.MutationReceipt)
	if first.Validate() != nil || first.Replayed {
		t.Fatal("confirmed runtime receipt invalid")
	}
	read := authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: first.Citation.EntityID, Operation: "get"}
	result := dispatchFixture(t, r, "Read", read).(authority.ReadResult)
	if result.Validate() != nil || len(result.Entities) != 1 || result.Entities[0].Citation != first.Citation {
		t.Fatal("runtime read changed citation")
	}
	metadata := dispatchFixture(t, r, "RevisionMetadata", first.Citation).(RevisionMetadata)
	if metadata.Validate() != nil || metadata.ActorID != mutation.ActorID || metadata.Citation != first.Citation {
		t.Fatal("asserted actor lost in protected metadata")
	}
	original := authority.OriginalOperationRequest{ProjectID: testProject, IdempotencyKey: mutation.Request.Intent.IdempotencyKey}
	receipt := dispatchFixture(t, r, "MutationReceipt", original).(authority.MutationReceipt)
	if receipt != first {
		t.Fatal("receipt inspection altered original acknowledgement")
	}
	auditRequest := authority.AuditRequest{ProjectID: testProject, Limit: 100}
	audit := dispatchFixture(t, r, "Audit", auditRequest).(AuditResult)
	if audit.Match(auditRequest) != nil || len(audit.Entries) != 1 || audit.Entries[0].OperationID != first.OperationID {
		t.Fatal("protected audit project/operation binding lost")
	}
	status := dispatchFixture(t, r, "Read", authority.ReadRequest{ProjectID: testProject, Operation: "status"}).(authority.ProjectStatus)
	if status.Validate() != nil || status.AuthorityGeneration != 1 || status.IndexedGeneration != 0 {
		t.Fatal("status claimed nonexistent projection or changed authority")
	}
	replayed := dispatchFixture(t, r, "MutateAttributed", mutation).(authority.MutationReceipt)
	if !replayed.Replayed || replayed.OperationID != first.OperationID {
		t.Fatal("explicit original-key replay duplicated mutation")
	}
	for _, change := range []string{"actor", "payload"} {
		candidate := mutation
		if change == "actor" {
			candidate.ActorID = "another-actor"
		} else {
			candidate.Request.Payload = json.RawMessage(`{"title":"Changed","body":"Evidence","state":"open"}`)
		}
		body, _ := json.Marshal(candidate)
		_, err := r.Dispatch(context.Background(), "MutateAttributed", body)
		var code authority.Error
		if !errors.As(err, &code) || code.Code != authority.Conflict {
			t.Fatal("changed original intent did not conflict")
		}
	}
	body, _ := json.Marshal(mutation.Request)
	_, err := r.Dispatch(context.Background(), "Mutate", body)
	var code authority.Error
	if !errors.As(err, &code) || code.Code != authority.Unsupported {
		t.Fatal("legacy Mutate fabricated attribution")
	}
	unknown := authority.ReadRequest{ProjectID: "22222222-2222-4222-8222-222222222222", Operation: "status"}
	body, _ = json.Marshal(unknown)
	_, err = r.Dispatch(context.Background(), "Read", body)
	if !errors.As(err, &code) || code.Code != authority.NoAuthority {
		t.Fatal("unknown project reached wrong namespace")
	}
	after := dispatchFixture(t, r, "Audit", auditRequest).(AuditResult)
	if len(after.Entries) != 1 {
		t.Fatal("read/replay/refusal created new authority work")
	}
}

func TestRuntimeDispatchOriginalDocumentRecoveryIsExplicitAndScoped(t *testing.T) {
	r := activeRuntime(t, true)
	store := runtimeStore(t, r)
	document := authority.DocumentMutationCall{ActorID: "synthetic-agent", Request: documentRequest(t, "document.create", "runtime-original-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# Exact original document")}
	store.mu.Lock()
	store.fault = func(phase string) error {
		if phase == "knowledge_ref_rename" {
			return Error{StorageUnavailable}
		}
		return nil
	}
	store.mu.Unlock()
	body, _ := json.Marshal(document)
	if _, err := r.Dispatch(context.Background(), "MutateDocument", body); err == nil {
		t.Fatal("interrupted document unexpectedly acknowledged")
	}
	store.mu.Lock()
	store.fault = nil
	store.mu.Unlock()
	original := authority.OriginalOperationRequest{ProjectID: testProject, IdempotencyKey: document.Request.Intent.IdempotencyKey}
	state := dispatchFixture(t, r, "DocumentOperationStatus", original).(DocumentOperationResult)
	if state.Match(original) != nil || state.State.Phase == "settled" {
		t.Fatal("status settled or lost original pending operation")
	}
	cleanup := authority.CleanupDocumentRequest{ProjectID: testProject, IdempotencyKey: original.IdempotencyKey, WriteMode: "local"}
	body, _ = json.Marshal(cleanup)
	_, err := r.Dispatch(context.Background(), "CleanupDocumentPin", body)
	var code Error
	if !errors.As(err, &code) || code.Code != DocumentPending {
		t.Fatal("cleanup settled a pending original operation")
	}
	tracker := authority.AttributedMutationRequest{ActorID: "synthetic-agent", Request: request(t, "issue.create", "runtime-independent-tracker", authority.IssuePayload{Title: "Intact tracker", State: "open"})}
	_ = dispatchFixture(t, r, "MutateAttributed", tracker).(authority.MutationReceipt)
	before := dispatchFixture(t, r, "DocumentOperationStatus", original).(DocumentOperationResult)
	if before.State.Phase == "settled" || before.State.OperationID != state.State.OperationID {
		t.Fatal("unrelated tracker operation cleared document fence")
	}
	settled := dispatchFixture(t, r, "MutateDocument", document).(authority.DocumentMutationResult)
	if settled.Validate() != nil || settled.Receipt.OperationID != state.State.OperationID {
		t.Fatal("original replay changed document operation")
	}
	cleaned := dispatchFixture(t, r, "CleanupDocumentPin", cleanup).(DocumentOperationResult)
	if cleaned.Match(original) != nil || cleaned.State.Phase != "settled" || cleaned.State.PinState != "cleared" {
		t.Fatal("explicit settled pin cleanup invalid")
	}
}

func TestRuntimeDispatchContendedLaneCancellationHasNoAuthorityEffects(t *testing.T) {
	r := activeRuntime(t, false)
	r.mu.RLock()
	entry := r.projects[testProject]
	r.mu.RUnlock()
	<-entry.lane
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	body, _ := json.Marshal(authority.AttributedMutationRequest{ActorID: "synthetic-agent", Request: request(t, "issue.create", "cancelled-runtime-key", authority.IssuePayload{Title: "Never admitted", State: "open"})})
	_, err := r.Dispatch(ctx, "MutateAttributed", body)
	entry.lane <- struct{}{}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("contended admission did not retain caller cancellation")
	}
	status := dispatchFixture(t, r, "Read", authority.ReadRequest{ProjectID: testProject, Operation: "status"}).(authority.ProjectStatus)
	if status.AuthorityGeneration != 0 {
		t.Fatal("cancelled lane admission mutated authority")
	}
}

func TestRuntimeDispatchShutdownJoinsIndependentDomainCall(t *testing.T) {
	r, registry, directory, control := runtimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	admitted := make(chan struct{})
	go func() { done <- r.Serve(ctx, func(ctx context.Context, _ net.Conn) { close(admitted); <-ctx.Done() }) }()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	joined := false
	t.Cleanup(func() {
		cancel()
		unblock()
		if !joined {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("shutdown fixture did not join")
			}
		}
	})
	client := runtimeClient(t, ctx, control)
	defer client.Close()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("shutdown handler not admitted")
	}
	store := runtimeStore(t, r)
	store.mu.Lock()
	store.fault = func(phase string) error {
		if phase == "acknowledgement" {
			close(entered)
			<-release
		}
		return nil
	}
	store.mu.Unlock()
	mutation := authority.AttributedMutationRequest{ActorID: "synthetic-agent", Request: request(t, "issue.create", "runtime-shutdown-key", authority.IssuePayload{Title: "Original committed intent", State: "open"})}
	body, _ := json.Marshal(mutation)
	callDone := make(chan error, 1)
	go func() { _, err := r.Dispatch(context.Background(), "MutateAttributed", body); callDone <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("domain call did not reach acknowledgement")
	}
	cancel()
	select {
	case <-done:
		joined = true
		t.Fatal("shutdown released direct domain call before join")
	case <-time.After(20 * time.Millisecond):
	}
	if writer, err := OpenWriter(context.Background(), registry, directory, testProject); err == nil {
		_ = writer.Close()
		t.Fatal("shutdown released active domain UUID lease")
	}
	unblock()
	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("runtime stop did not cancel domain acknowledgement")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("domain call did not join")
	}
	select {
	case <-done:
		joined = true
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not join after domain completion")
	}
	writer, err := OpenWriter(context.Background(), registry, directory, testProject)
	if err != nil {
		t.Fatal("joined runtime retained UUID lease")
	}
	defer writer.Close()
	receipt, err := writer.Mutate(context.Background(), mutation.Request, Actor{ID: mutation.ActorID})
	if err != nil || !receipt.Replayed || receipt.ProjectSequence != 1 {
		t.Fatal("cancelled acknowledgement lost or duplicated original committed intent")
	}
}

func TestRuntimeDispatchAdmissionHasFiniteGlobalBound(t *testing.T) {
	r := activeRuntime(t, false)
	r.mu.RLock()
	entry := r.projects[testProject]
	r.mu.RUnlock()
	<-entry.lane
	var released sync.Once
	unblock := func() { released.Do(func() { entry.lane <- struct{}{} }) }
	ctx, cancel := context.WithCancel(context.Background())
	body, _ := json.Marshal(authority.ReadRequest{ProjectID: testProject, Operation: "status"})
	var workers sync.WaitGroup
	errorsSeen := make(chan error, 32)
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); _, err := r.Dispatch(ctx, "Read", body); errorsSeen <- err }()
	}
	joined := make(chan struct{})
	go func() { workers.Wait(); close(joined) }()
	t.Cleanup(func() {
		cancel()
		unblock()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Error("bounded dispatch workers did not join")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for len(r.calls) != 32 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(r.calls) != 32 {
		t.Fatal("bounded admission fixture did not fill")
	}
	_, err := r.Dispatch(context.Background(), "Read", body)
	var code Error
	if !errors.As(err, &code) || code.Code != Capacity {
		t.Fatal("shared domain admission exceeded finite global bound")
	}
	cancel()
	unblock()
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled bounded workers did not join")
	}
	for i := 0; i < 32; i++ {
		if err := <-errorsSeen; !errors.Is(err, context.Canceled) {
			t.Fatal("contended bounded worker ignored cancellation")
		}
	}
	status := dispatchFixture(t, r, "Read", authority.ReadRequest{ProjectID: testProject, Operation: "status"}).(authority.ProjectStatus)
	if status.AuthorityGeneration != 0 || len(r.calls) != 0 {
		t.Fatal("bounded admission leaked calls or authority effects")
	}
}
