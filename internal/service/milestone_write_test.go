package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

type milestoneConfirmationClient struct {
	*fakeGitCodeClient
	writes, reads int
	m             gitcode.Milestone
	err           error
	onWrite       func()
}

func (c *milestoneConfirmationClient) CreateMilestone(context.Context, gitcode.MilestoneWriteRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Milestone], error) {
	c.writes++
	if c.onWrite != nil {
		c.onWrite()
	}
	return gitcode.WriteResult[gitcode.Milestone]{Record: c.m, Confirmed: c.err == nil, RemoteID: c.m.RemoteID, RemoteRevision: c.m.UpdatedAt}, c.err
}
func (c *milestoneConfirmationClient) UpdateMilestone(ctx context.Context, r gitcode.MilestoneWriteRequest, o gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Milestone], error) {
	return c.CreateMilestone(ctx, r, o)
}
func (c *milestoneConfirmationClient) GetMilestone(context.Context, gitcode.MilestoneRequest) (gitcode.Milestone, error) {
	c.reads++
	return c.m, nil
}

func TestMilestoneCommandInvalidFieldsNoSideEffects(t *testing.T) {
	for _, mode := range []WriteMode{WriteModeDryRun, WriteModeLive} {
		for _, command := range []string{"create-milestone", "update-milestone"} {
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedStore(t, ctx, store)
			client := &milestoneConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{}}
			svc := NewWithClient(store, client)
			req := WriteCommandRequest{RepoID: "fixture-a", Mode: mode, Milestone: "7", Title: "Fixture", DueOn: "2026-12-31", Description: strings.Repeat("😀", 1001), IdempotencyKey: "invalid"}
			_, err = milestoneCall(svc, ctx, command, req)
			var invalid ErrInvalidQuery
			if !errors.As(err, &invalid) || invalid.Field != "milestone.description" {
				t.Fatalf("invalid fields: %v", err)
			}
			entry, _ := store.GetAuditEventByKey(ctx, "fixture-a", "invalid")
			if client.writes != 0 || client.reads != 0 || entry != nil {
				t.Fatal("validation had side effects")
			}
		}
	}
}
func milestoneCall(s *Service, ctx context.Context, command string, r WriteCommandRequest) (WriteCommandResult, error) {
	if command == "create-milestone" {
		return s.CreateMilestone(ctx, r)
	}
	return s.UpdateMilestone(ctx, r)
}

func TestMilestoneClaimRecoveryAndAtomicPublication(t *testing.T) {
	for _, command := range []string{"create-milestone", "update-milestone"} {
		t.Run(command, func(t *testing.T) {
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedStore(t, ctx, store)
			t.Setenv("GITCODE_TOKEN", "test-token")
			client := &milestoneConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{}, m: gitcode.Milestone{RemoteID: "7", SourceID: "MILESTONE-7", Title: "Fixture", Body: "wanted", DueOn: "2026-12-31", Status: "open", UpdatedAt: "2026-10-01T00:00:00Z"}, err: gitcode.ErrMilestoneWrite{RemoteID: "7", Cause: gitcode.ErrWriteMutationPhase{Phase: "readback", MutationAttempted: true, Cause: errors.New("opaque-private-provider-body")}}}
			client.onWrite = func() {
				e, _ := store.GetAuditEventByKey(ctx, "fixture-a", "milestone-key")
				if e == nil || e.Status != audit.StatusInProgress {
					t.Error("mutation without durable claim")
				}
			}
			req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Milestone: "7", Title: "Fixture", Description: "wanted", DueOn: "2026-12-31", IdempotencyKey: "milestone-key"}
			svc := NewWithClient(store, client)
			_, err = milestoneCall(svc, ctx, command, req)
			var failure ErrWriteFailure
			if !errors.As(err, &failure) || failure.Code != "write_ambiguous_remote" || strings.Contains(err.Error(), "opaque-private") {
				t.Fatalf("unsafe failure: %v", err)
			}
			client.m.Body = "different"
			_, err = milestoneCall(NewWithClient(store, client), ctx, command, req)
			if err == nil || client.writes != 1 {
				t.Fatal("mismatch repeated mutation or succeeded")
			}
			client.m.Body = "wanted"
			fault := &wikiFaultStore{SQLiteStore: store, graphErr: errors.New("publication failed")}
			_, err = milestoneCall(NewWithClient(fault, client), ctx, command, req)
			if err == nil {
				t.Fatal("failed cache publication succeeded")
			}
			e, _ := store.GetAuditEventByKey(ctx, "fixture-a", "milestone-key")
			if e.Status != audit.StatusRemoteConfirmedCacheRefreshPending {
				t.Fatal("pending settlement not retained")
			}
			result, err := milestoneCall(NewWithClient(store, client), ctx, command, req)
			if err != nil || result.Status != "recovered_after_ambiguous_write" || result.RemoteID != "7" || client.writes != 1 {
				t.Fatalf("recovery: %+v %v writes=%d", result, err, client.writes)
			}
			source, err := store.GetSourceScoped(ctx, "fixture-a", "MILESTONE-7")
			if err != nil || !strings.Contains(source.Body, "wanted") {
				t.Fatal("canonical graph missing")
			}
			result, err = milestoneCall(NewWithClient(store, client), ctx, command, req)
			if err != nil || result.RemoteRevision != client.m.UpdatedAt || client.writes != 1 {
				t.Fatal("settled replay lost identity or repeated write")
			}
			req.Description = "changed"
			_, err = milestoneCall(svc, ctx, command, req)
			if err == nil || client.writes != 1 {
				t.Fatal("key conflict accepted")
			}
		})
	}
}

func TestMilestoneUnknownCreateIdentityRemainsFenced(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	t.Setenv("GITCODE_TOKEN", "test-token")
	client := &milestoneConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{}, err: gitcode.ErrMilestoneWrite{Cause: gitcode.ErrWriteMutationPhase{Phase: "post", MutationAttempted: true, Cause: errors.New("timeout")}}}
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Fixture", DueOn: "2026-12-31", IdempotencyKey: "unknown-create"}
	for i := 0; i < 2; i++ {
		if _, err := NewWithClient(store, client).CreateMilestone(ctx, req); err == nil {
			t.Fatal("unknown identity confirmed")
		}
	}
	if client.writes != 1 || client.reads != 0 {
		t.Fatal("unknown create retried or guessed identity")
	}
}

func TestMilestoneWhitespaceDescriptionRecovery(t *testing.T) {
	for _, command := range []string{"create-milestone", "update-milestone"} {
		t.Run(command, func(t *testing.T) {
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedStore(t, ctx, store)
			t.Setenv("GITCODE_TOKEN", "test-token")
			var m gitcode.Milestone
			if err := json.Unmarshal([]byte(`{"id":7,"title":"Fixture","description":" \n\t ","body":"wrong fallback","due_on":"2026-12-31","state":"open"}`), &m); err != nil {
				t.Fatal(err)
			}
			client := &milestoneConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{}, m: m, err: gitcode.ErrMilestoneWrite{RemoteID: "7", Cause: gitcode.ErrWriteMutationPhase{Phase: "readback", MutationAttempted: true, Cause: errors.New("timeout")}}}
			req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Milestone: "7", Title: "Fixture", Description: " \n\t ", DueOn: "2026-12-31", IdempotencyKey: "whitespace-recovery"}
			if _, err := milestoneCall(NewWithClient(store, client), ctx, command, req); err == nil {
				t.Fatal("initial ambiguous write unexpectedly succeeded")
			}
			readsBeforeRecovery := client.reads
			result, err := milestoneCall(NewWithClient(store, client), ctx, command, req)
			if err != nil || result.Status != "recovered_after_ambiguous_write" || client.writes != 1 || client.reads-readsBeforeRecovery != 1 {
				t.Fatalf("whitespace GET-only recovery failed: status=%s writes=%d reads=%d err=%v", result.Status, client.writes, client.reads, err)
			}
		})
	}
}

func TestMilestoneLateStagingCannotOverwriteNewerRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	t.Setenv("GITCODE_TOKEN", "test-token")
	client := &milestoneConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{}, m: gitcode.Milestone{RemoteID: "7", SourceID: "MILESTONE-7", Title: "Fixture", Body: "wanted", DueOn: "2026-12-31", Status: "open", UpdatedAt: "2026-10-01T00:00:00Z"}}
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Milestone: "7", Title: "Fixture", Description: "wanted", DueOn: "2026-12-31", IdempotencyKey: "staging-race"}
	fault := &wikiFaultStore{SQLiteStore: store}
	once := false
	fault.onStage = func(_, _ cache.AuditTrailEntry) {
		if once {
			return
		}
		once = true
		client.m.UpdatedAt = "2026-10-02T00:00:00Z"
		result, err := NewWithClient(store, client).UpdateMilestone(ctx, req)
		if err != nil || result.Status != "recovered_after_ambiguous_write" {
			t.Fatalf("concurrent recovery=%+v %v", result, err)
		}
	}
	result, err := NewWithClient(fault, client).UpdateMilestone(ctx, req)
	if err != nil || result.RemoteRevision != client.m.UpdatedAt || client.writes != 1 {
		t.Fatalf("late stage overwrote recovery: %+v %v writes=%d", result, err, client.writes)
	}
	source, err := store.GetSourceScoped(ctx, "fixture-a", "MILESTONE-7")
	if err != nil || source.UpdatedAt.Format(time.RFC3339) != client.m.UpdatedAt {
		t.Fatal("cache lost newer revision")
	}
}
