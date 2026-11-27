package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
)

func TestLocalIssueMetadataPredicatesBeforePagination(t *testing.T) {
	host, _, _ := localSearchFixture(t)
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "API closed", Body: "needle", Labels: []string{"bug", "needs review, API"}, State: "closed", IdempotencyKey: "closed"})
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "API body-only", Body: "bug needs review, API", Labels: []string{"feature"}, IdempotencyKey: "body-only"})
	third := searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "API [核心]. Ω", Body: "needle", Labels: []string{"bug", "needs review, API"}, IdempotencyKey: "matching"})
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Other", Body: "needle", Labels: []string{"bug"}, IdempotencyKey: "other"})
	var q LocalProjectQuery
	if json.Unmarshal([]byte(`{"project":"lab","kind":"issue","limit":1,"status":"open","labels":["bug","needs review, API"],"title_contains":"[核心]. Ω"}`), &q) != nil {
		t.Fatal("invalid fixture")
	}
	list, err := host.List(context.Background(), q)
	if err != nil || len(list) != 1 || list[0].Metadata.ID != third.Metadata.ID {
		t.Fatal("metadata predicate was ignored or applied after pagination")
	}
}

func TestLocalIssueMetadataMatchingAndInvalidInputs(t *testing.T) {
	host, _, _ := localSearchFixture(t)
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "API closed", State: "closed", Body: "needle", Labels: []string{"bug", "needs review, API"}, IdempotencyKey: "closed"})
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "API body-only", Body: "bug needs review, API", Labels: []string{"feature"}, IdempotencyKey: "body"})
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "API [核心]. Ω", Body: "needle", Labels: []string{"bug", "needs review, API"}, IdempotencyKey: "match"})
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Other", Body: "needle", Labels: []string{"bug"}, IdempotencyKey: "other"})
	for _, tc := range []struct {
		q   LocalProjectQuery
		ids string
	}{
		{q: LocalProjectQuery{Status: "open", Labels: []string{"bug"}, TitleContains: "API"}, ids: "ISSUE-3"},
		{q: LocalProjectQuery{Status: "open", Labels: []string{"feature", "needs review, API"}, LabelsMatch: "any"}, ids: "ISSUE-2 ISSUE-3"},
		{q: LocalProjectQuery{Labels: []string{"bug", "needs review, API"}}, ids: "ISSUE-1 ISSUE-3"},
		{q: LocalProjectQuery{Status: "open", Labels: []string{"bug"}, Offset: 1, Limit: 1}, ids: "ISSUE-4"},
		{q: LocalProjectQuery{Status: "open", Labels: []string{"bug"}, Offset: 2}, ids: ""},
		{q: LocalProjectQuery{TitleContains: "[核心]. Ω"}, ids: "ISSUE-3"},
		{q: LocalProjectQuery{TitleContains: "api"}, ids: ""},
		{q: LocalProjectQuery{TitleContains: "[.*]"}, ids: ""},
		{q: LocalProjectQuery{TitleContains: "body-only", Labels: []string{"bug"}}, ids: ""},
		{q: LocalProjectQuery{Labels: []string{"bugs"}}, ids: ""},
		{q: LocalProjectQuery{Status: "unknown"}, ids: ""},
	} {
		tc.q.Project, tc.q.Kind = "lab", localgit.Issue
		if tc.q.Limit == 0 {
			tc.q.Limit = 20
		}
		got, err := host.List(context.Background(), tc.q)
		if err != nil {
			t.Fatal("valid metadata query failed")
		}
		ids := []string{}
		for _, item := range got {
			ids = append(ids, item.Metadata.ID)
		}
		if strings.Join(ids, " ") != tc.ids {
			t.Fatalf("metadata match IDs=%v want=%s", ids, tc.ids)
		}
	}
	for _, q := range []LocalProjectQuery{
		{Labels: []string{"bug", "bug"}}, {Labels: []string{" "}}, {Labels: []string{"line\nbreak"}}, {Labels: make([]string, 33)},
		{Status: "\x00"}, {TitleContains: string([]byte{0xff})}, {TitleContains: strings.Repeat("Ω", 513)}, {LabelsMatch: "ALL"},
		{Status: "open", Offset: -1}, {Status: "open", Limit: 129}, {Status: "open", Kind: localgit.Wiki},
	} {
		q.Project = "lab"
		if q.Kind == "" {
			q.Kind = localgit.Issue
		}
		if q.Limit == 0 {
			q.Limit = 20
		}
		if _, err := host.List(context.Background(), q); err == nil {
			t.Fatal("invalid metadata query accepted")
		}
	}
	for _, get := range []bool{true, false} {
		q := LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: "ISSUE-1", Status: "open", MaxCommits: 10}
		var err error
		if get {
			_, err = host.Get(context.Background(), q)
		} else {
			_, err = host.History(context.Background(), q)
		}
		if err == nil {
			t.Fatal("metadata predicates silently affected get/history")
		}
	}
}

func TestLocalIssueMetadataSearchNoIndexAndExactPrefixCitation(t *testing.T) {
	host, cfg, path := localSearchFixture(t)
	issue := searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "核心 title", Labels: []string{"bug, exact"}, Body: strings.Repeat("Ω evidence\n", 100), IdempotencyKey: "one"})
	before, _ := host.Status(context.Background())
	for _, mode := range []string{"fulltext", "hybrid"} {
		var q LocalProjectSearchRequest
		_ = json.Unmarshal([]byte(`{"project":"lab","labels":["bug, exact"],"status":"open","title_contains":"核心"}`), &q)
		q.Mode = mode
		r, err := host.Search(context.Background(), q)
		if err != nil || !r.MetadataOnly || r.EffectiveMode != "fulltext" || r.IndexState != "not_requested" || len(r.Hits) != 1 {
			t.Fatal("metadata-only query used an index/provider or lost a match")
		}
		hit := r.Hits[0]
		if hit.Citation.ID != issue.Metadata.ID || hit.Citation.Blob != issue.Citation.Blob || hit.Citation.Commit != r.Commit {
			t.Fatal("metadata citation lost Git identity")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "--git-dir="+path, "cat-file", "blob", hit.Citation.Blob)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
		raw, err := cmd.Output()
		if err != nil || len(hit.Snippet) > 800 || !bytes.HasPrefix(raw, []byte(hit.Snippet)) || hit.LineStart != 1 || hit.LineEnd != 1+strings.Count(hit.Snippet, "\n") {
			t.Fatal("metadata snippet is not the exact cited Git prefix")
		}
		digest := sha256.Sum256([]byte(hit.Snippet))
		if hit.Digest != hex.EncodeToString(digest[:]) {
			t.Fatal("metadata digest is not its source slice digest")
		}
	}
	for _, target := range []string{cfg.CachePath, localProjectIndexPath(cfg)} {
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("metadata-only query created SQLite state")
		}
	}
	after, _ := host.Status(context.Background())
	if before.Projects[0].Commit != after.Projects[0].Commit {
		t.Fatal("metadata read modified Git")
	}
	for _, q := range []LocalProjectSearchRequest{{Project: "lab"}, {Project: "lab", Labels: []string{}}, {Project: "lab", LabelsMatch: "any"}, {Project: "lab", Labels: []string{"bug", "bug"}}} {
		if _, err := host.Search(context.Background(), q); err == nil {
			t.Fatal("empty/invalid metadata-only query accepted")
		}
	}
}

func TestLocalIssueMetadataProjectIsolation(t *testing.T) {
	_, cfg, path := localSearchFixture(t)
	otherPath := filepath.Join(t.TempDir(), "other.git")
	if _, err := localgit.Initialize(context.Background(), otherPath, "other", "refs/heads/main"); err != nil {
		t.Fatal("second project fixture unavailable")
	}
	host, err := NewLocalProjects(context.Background(), map[string]config.LocalProjectConfig{
		"lab":   {Backend: "local_git", ProjectID: "lab", StorageRef: "first", Ref: "refs/heads/main"},
		"other": {Backend: "local_git", ProjectID: "other", StorageRef: "second", Ref: "refs/heads/main"},
	}, func(ref string) (string, error) {
		if ref == "first" {
			return path, nil
		}
		return otherPath, nil
	})
	if err != nil {
		t.Fatal("isolated registrations unavailable")
	}
	host.WithSearchConfig(cfg)
	first := searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Local evidence", Labels: []string{"first"}, IdempotencyKey: "first"})
	status, _ := host.Status(context.Background())
	head := ""
	for _, p := range status.Projects {
		if p.Project == "other" {
			head = p.Commit
		}
	}
	second, err := host.Mutate(context.Background(), LocalProjectMutation{Project: "other", Mutation: localgit.Mutation{Operation: "create_issue", Title: "Other evidence", Labels: []string{"second"}, ExpectedHead: head, WriteMode: "local", IdempotencyKey: "second"}})
	if err != nil {
		t.Fatal("second entity unavailable")
	}
	for _, project := range []string{"lab", "other"} {
		r, err := host.Search(context.Background(), LocalProjectSearchRequest{Project: project, Labels: []string{"second"}})
		if err != nil {
			t.Fatal("isolated metadata query failed")
		}
		if project == "lab" {
			if len(r.Hits) != 0 {
				t.Fatal("metadata crossed project authority")
			}
		} else if len(r.Hits) != 1 || r.Hits[0].Citation.ProjectID != "other" || r.Hits[0].Citation.Blob != second.Entity.Citation.Blob || r.Hits[0].Citation.Blob == first.Citation.Blob {
			t.Fatal("project-local same stable ID lost identity")
		}
	}
}

func TestLocalIssueMetadataTextHybridAndFallbackKeepEligibility(t *testing.T) {
	host, cfg, _ := localSearchFixture(t)
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Closed", State: "closed", Body: "needle needle needle", Labels: []string{"bug"}, IdempotencyKey: "closed"})
	want := searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Eligible", Body: "needle", Labels: []string{"bug"}, IdempotencyKey: "open"})
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_wiki", Title: "Unfiltered wiki", Body: "needle", IdempotencyKey: "wiki"})
	q := LocalProjectSearchRequest{Project: "lab", Query: "needle", Status: "open", Labels: []string{"bug"}, Mode: "hybrid", Limit: 1}
	check := func(wantMode string) {
		t.Helper()
		r, err := host.Search(context.Background(), q)
		if err != nil || r.EffectiveMode != wantMode || len(r.Hits) != 1 || r.Hits[0].Citation.Blob != want.Citation.Blob || r.Hits[0].Citation.ID != want.Metadata.ID {
			t.Fatal("filtered textual/hybrid eligibility or citation changed")
		}
	}
	check("fulltext")
	if _, err := host.Index(context.Background(), LocalProjectIndexRequest{Project: "lab", WriteMode: "local"}); err != nil {
		t.Fatal("whole-project fixture index failed")
	}
	check("hybrid")
	if os.Rename(localProjectIndexPath(cfg), localProjectIndexPath(cfg)+".removed") != nil {
		t.Fatal("fixture index loss unavailable")
	}
	check("fulltext")
	q.Labels = []string{"absent"}
	r, err := host.Search(context.Background(), q)
	if err != nil || len(r.Hits) != 0 {
		t.Fatal("fallback returned an ineligible body match")
	}
	if os.Rename(localProjectIndexPath(cfg)+".removed", localProjectIndexPath(cfg)) != nil {
		t.Fatal("fixture index restore unavailable")
	}
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "update_issue", ID: want.Metadata.ID, Title: "Eligible", State: "open", Body: "needle", Labels: []string{"not-bug"}, IdempotencyKey: "remove-label"})
	q.Labels = []string{"bug"}
	r, err = host.Search(context.Background(), q)
	if err != nil || r.EffectiveMode != "fulltext" || r.IndexState != "stale" || len(r.Hits) != 0 {
		t.Fatal("stale index revived removed label membership")
	}
}
