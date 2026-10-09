package service

import (
	"context"
	"encoding/json"
	"errors"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"testing"
)

func TestMilestoneCanonicalLocatorCacheAndReplay(t *testing.T) {
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
			if err := json.Unmarshal([]byte(`{"number":123456,"title":"Fixture","description":"safe","due_on":"2026-12-31","url":"https://example.invalid/owner/repo/milestones/7"}`), &m); err != nil {
				t.Fatal(err)
			}
			client := &milestoneConfirmationClient{fakeGitCodeClient: &fakeGitCodeClient{listMilestonesResult: gitcode.Page[gitcode.Milestone]{Items: []gitcode.Milestone{m}}}, m: m}
			svc := NewWithClient(store, client)
			listed, err := svc.ListMilestones(ctx, MilestoneListRequest{RepoID: "fixture-a"})
			if err != nil || listed.Milestones[0].IID != "7" || listed.Milestones[0].BrowserURL != m.HTMLURL {
				t.Fatalf("list locator missing: %+v %v", listed, err)
			}
			projectionErr := errors.New("projection unavailable")
			if _, err := NewWithClient(&wikiFaultStore{SQLiteStore: store, graphErr: projectionErr}, client).ListMilestones(ctx, MilestoneListRequest{RepoID: "fixture-a"}); !errors.Is(err, projectionErr) {
				t.Fatal("failed mapping publication reported as fresh")
			}
			req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Milestone: "123456", Title: "Fixture", Description: "safe", DueOn: "2026-12-31", IdempotencyKey: "locator-replay"}
			first, err := milestoneCall(svc, ctx, command, req)
			if err != nil {
				t.Fatal(err)
			}
			reads := client.reads
			replay, err := milestoneCall(NewWithClient(store, client), ctx, command, req)
			if err != nil || first.BrowserURL != m.HTMLURL || replay.BrowserURL != m.HTMLURL || client.writes != 1 || client.reads != reads {
				t.Fatalf("locator replay=%+v err=%v", replay, err)
			}
			aliases, err := store.GetIdentityMapScoped(ctx, "fixture-a", "MILESTONE-123456")
			if err != nil {
				t.Fatal(err)
			}
			foundURL, foundIID := false, false
			for _, a := range aliases {
				if a.Remote.ID != "123456" {
					t.Fatal("local iid replaced provider identity")
				}
				foundURL = foundURL || a.AliasType == "url" && a.Alias == m.HTMLURL
				foundIID = foundIID || a.AliasType == "milestone_iid" && a.Alias == "milestone_iid:7"
			}
			if !foundURL || !foundIID {
				t.Fatal("canonical locator mapping not durable")
			}
			if milestoneRecord(m).IID != "7" {
				t.Fatal("list DTO lost iid")
			}
		})
	}
}
