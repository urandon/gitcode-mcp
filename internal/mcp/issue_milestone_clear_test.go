package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func TestIntegrationIssueMilestoneClearReadbackAndReplay(t *testing.T) {
	for _, ignoreClear := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "partial state only"}[ignoreClear], func(t *testing.T) {
			var patches, gets atomic.Int32
			var closed, cleared atomic.Bool
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/api/v5/repos/owner/repo/issues/42" {
					t.Error("unexpected provider route")
					w.WriteHeader(404)
					return
				}
				switch r.Method {
				case "PATCH":
					patches.Add(1)
					var p map[string]json.RawMessage
					if json.NewDecoder(r.Body).Decode(&p) != nil || string(p["state"]) != `"close"` || string(p["milestone"]) != "0" {
						t.Error("incorrect combined clear wire contract")
					}
					closed.Store(true)
					cleared.Store(!ignoreClear)
				case "GET":
					gets.Add(1)
				default:
					t.Error("unexpected provider method")
				}
				state := "open"
				if closed.Load() {
					state = "closed"
				}
				var milestone any = map[string]any{"id": 7, "title": "Fixture release", "state": "open"}
				if cleared.Load() {
					milestone = nil
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "42", "number": 42, "title": "Fixture issue", "body": "Original fixture body", "state": state, "labels": []any{}, "milestone": milestone})
			}))
			defer api.Close()
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{
				Record:           cache.Record{RepoID: "fixture", ID: "ISSUE-42", Type: "issue", Path: "issues/42.md", Title: "Fixture issue", Status: "open", ContentHash: "before-clear", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "42", CreatedAt: now, UpdatedAt: now},
				RelatedRecords:   []cache.Record{{RepoID: "fixture", ID: "MILESTONE-7", Type: "milestone", Path: "milestones/7.md", Title: "Fixture release", Status: "open", ContentHash: "milestone", Provenance: cache.ProvenanceRemote, RemoteType: "milestone", RemoteID: "7", CreatedAt: now, UpdatedAt: now}},
				ReplaceLinkKinds: []string{"milestone"}, Links: []cache.Link{{TargetID: "MILESTONE-7", Kind: "milestone", Text: "Fixture release"}},
			}); err != nil {
				t.Fatal(err)
			}
			svc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", service.ServiceConfig{BaseURL: api.URL})
			if err != nil {
				t.Fatal(err)
			}
			h := NewRPCHandlerWithToolAccess(svc, ToolAccessWrite)
			args := map[string]any{"repo_id": "fixture", "number": 42, "state": "closed", "clear_milestone": true, "write_mode": "live", "idempotency_key": "combined-clear"}
			first := mergeRPC(t, h, "update_issue", args)
			if ignoreClear {
				if first.Error == nil || first.Error.Data.Code != "write_ambiguous_remote" {
					t.Fatal("partial mutation reported success")
				}
				observation := first.Error.Data.IssueReadback
				if observation == nil || observation.State != "closed" || !observation.MilestonePresent || observation.MilestoneMatches {
					t.Fatal("partial state/milestone evidence missing")
				}
			} else if first.Error != nil {
				t.Fatalf("combined clear: %v", first.Error)
			}
			beforeRetry := gets.Load()
			retry := mergeRPC(t, h, "update_issue", args)
			entry, err := store.GetAuditEventByKey(ctx, "fixture", "combined-clear")
			if err != nil || entry == nil || patches.Load() != 1 {
				t.Fatal("missing audit or duplicate mutation")
			}
			if ignoreClear {
				if retry.Error == nil || retry.Error.Data.Code != "write_ambiguous_remote" || entry.Status != audit.StatusInProgress || gets.Load() <= beforeRetry {
					t.Fatal("ambiguous replay did not remain GET-only and fenced")
				}
				if retry.Error.Data.IssueReadback != nil {
					t.Fatal("retry reused stale readback evidence")
				}
				cached, err := svc.GetSource(ctx, service.GetSourceRequest{RepoID: "fixture", ID: "ISSUE-42"})
				if err != nil || cached.Status != "open" || len(cached.Links) != 1 {
					t.Fatal("partial write published a successful cache update")
				}
			} else {
				if retry.Error != nil || entry.Status != audit.StatusSucceeded || gets.Load() != beforeRetry {
					t.Fatal("settled replay contacted provider")
				}
				links, err := store.ListLinks(ctx, cache.LinkFilter{RepoID: "fixture", SourceID: "ISSUE-42"})
				if err != nil || len(links) != 0 {
					t.Fatal("cleared milestone retained cached membership")
				}
			}
		})
	}
}
