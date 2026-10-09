package gitcode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMilestoneBrowserURLPreservesDistinctIdentities(t *testing.T) {
	var m Milestone
	if err := json.Unmarshal([]byte(`{"number":123456,"title":"Fixture","url":"https://gitcode.com/example-owner/example-repo/milestones/7"}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.RemoteID != "123456" || m.SourceID != "MILESTONE-123456" || m.IID != "7" || m.HTMLURL != "https://gitcode.com/example-owner/example-repo/milestones/123456?iid=7" {
		t.Fatalf("milestone identities/URL=%+v", m)
	}
}

func TestMilestoneBrowserIdentityShapesAndSanitization(t *testing.T) {
	for _, tc := range []struct {
		name, extra, iid, want string
		fail                   bool
	}{
		{"canonical", `"url":"https://example.invalid/owner/repo/milestones/123456?iid=7"`, "7", "https://example.invalid/owner/repo/milestones/123456?iid=7", false},
		{"explicit", `"iid":7,"url":"https://example.invalid/owner/repo/milestones/123456"`, "7", "https://example.invalid/owner/repo/milestones/123456?iid=7", false},
		{"no-invented-iid", `"url":"https://example.invalid/owner/repo/milestones/123456"`, "", "https://example.invalid/owner/repo/milestones/123456", false},
		{"credentials", `"url":"https://user:secret@example.invalid/owner/repo/milestones/7?token=secret#secret"`, "7", "https://example.invalid/owner/repo/milestones/123456?iid=7", false},
		{"explicit-no-url", `"iid":"7"`, "7", "", false},
		{"bad-iid", `"iid":-7`, "", "", true},
		{"conflict", `"iid":8,"url":"https://example.invalid/owner/repo/milestones/7"`, "", "", true},
		{"duplicate-query", `"url":"https://example.invalid/owner/repo/milestones/123456?iid=7&iid=8"`, "", "", true},
		{"bad-scheme", `"url":"javascript:secret"`, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m Milestone
			err := json.Unmarshal([]byte(`{"number":123456,"title":"Fixture",`+tc.extra+`}`), &m)
			if (err != nil) != tc.fail || !tc.fail && (m.IID != tc.iid || m.HTMLURL != tc.want) {
				t.Fatalf("iid=%q url=%q err=%v", m.IID, m.HTMLURL, err)
			}
			if strings.Contains(m.HTMLURL, "secret") || err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("secret locator material escaped")
			}
		})
	}
}

func TestMilestoneBrowserURLAcrossListGetCreateUpdate(t *testing.T) {
	const body = `{"number":123456,"title":"Fixture","description":"safe","due_on":"2026-12-31","url":"https://example.invalid/owner/repo/milestones/7"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/milestones") {
			fmt.Fprint(w, "["+body+"]")
			return
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	ctx := context.Background()
	check := func(m Milestone) {
		t.Helper()
		if m.RemoteID != "123456" || m.IID != "7" || m.HTMLURL != "https://example.invalid/owner/repo/milestones/123456?iid=7" {
			t.Fatalf("wrong locator %+v", m)
		}
	}
	page, err := client.ListMilestones(ctx, MilestoneListRequest{Owner: "owner", Repo: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	check(page.Items[0])
	m, err := client.GetMilestone(ctx, MilestoneRequest{Owner: "owner", Repo: "repo", ID: 123456})
	if err != nil {
		t.Fatal(err)
	}
	check(m)
	req := MilestoneWriteRequest{Owner: "owner", Repo: "repo", ID: 123456, Title: "Fixture", Description: "safe", DueOn: "2026-12-31"}
	for _, create := range []bool{true, false} {
		var result WriteResult[Milestone]
		if create {
			result, err = client.CreateMilestone(ctx, req, WriteOptions{IdempotencyKey: "create"})
		} else {
			result, err = client.UpdateMilestone(ctx, req, WriteOptions{IdempotencyKey: "update"})
		}
		if err != nil || !result.Confirmed {
			t.Fatalf("confirmation=%t err=%v", result.Confirmed, err)
		}
		check(result.Record)
		if result.BrowserURL != result.Record.HTMLURL {
			t.Fatal("write result locator policy differs")
		}
	}
}
