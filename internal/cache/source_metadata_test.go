package cache

import (
	"context"
	"fmt"
	"testing"
)

func TestMetadataPredicatesBeforeLimitAndSearch(t *testing.T) {
	for _, fts := range []bool{false, true} {
		t.Run(fmt.Sprint(fts), func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t, ctx)
			defer s.Close()
			if fts && !s.useFTS {
				t.Fatal("FTS fixture unavailable")
			}
			s.useFTS = fts
			mustAddTestRepo(t, ctx, s, "fixture-b")
			for i := 0; i < 45; i++ {
				source := testSource(fmt.Sprintf("ISSUE-%03d", i), "issue", "Bootstrap %_中文")
				source.Status = "closed"
				source.Body = "needle bug Bootstrap"
				source.Labels = []string{"bug"}
				if i >= 40 {
					source.Status = "open"
					source.Labels = []string{"bug", "area: 中文, one"}
				}
				mustUpsertGraph(t, ctx, s, SourceGraph{Source: source})
			}
			other := testSource("ISSUE-X", "issue", "Bootstrap %_中文")
			other.RepoID = "fixture-b"
			other.Status = "open"
			other.Labels = []string{"bug"}
			mustUpsertGraph(t, ctx, s, SourceGraph{Source: other})
			for _, match := range []string{"all", "any"} {
				got, err := s.ListSources(ctx, SourceFilter{RepoID: "fixture-a", Status: "open", Labels: []string{"bug", "area: 中文, one"}, LabelsMatch: match, TitleContains: "%_中文", Limit: 2})
				if err != nil || len(got) != 2 || got[0].ID != "ISSUE-040" {
					t.Fatalf("list: %v %#v", err, got)
				}
			}
			got, err := s.SearchSources(ctx, SearchQuery{RepoID: "fixture-a", Query: "needle", Status: "open", Labels: []string{"area: 中文, one"}, TitleContains: "%_中文", Limit: 2})
			if err != nil || len(got) != 2 || got[0].ID != "ISSUE-040" {
				t.Fatalf("search: %v %#v", err, got)
			}
			for _, label := range []string{"Bug", "missing", "%", " 中文"} {
				got, err := s.ListSources(ctx, SourceFilter{RepoID: "fixture-a", Labels: []string{label}})
				if err != nil || len(got) != 0 {
					t.Fatalf("literal label %q: %v %#v", label, err, got)
				}
			}
			// Nil labels are unknown, and body-only/title-only appearances are not membership.
			unknown := testSource("ISSUE-U", "issue", "bug")
			unknown.Status = "open"
			unknown.Labels = nil
			mustUpsertGraph(t, ctx, s, SourceGraph{Source: unknown})
			bodyOnly := testSource("ISSUE-Z", "issue", "Unrelated title")
			bodyOnly.Status = "open"
			bodyOnly.Body = "Bootstrap %_中文 bug"
			bodyOnly.Labels = []string{"bug"}
			mustUpsertGraph(t, ctx, s, SourceGraph{Source: bodyOnly})
			changed, err := s.GetSourceScoped(ctx, "fixture-a", "ISSUE-040")
			if err != nil {
				t.Fatal(err)
			}
			changed.Labels = []string{}
			mustUpsertGraph(t, ctx, s, SourceGraph{Source: changed})
			gotList, err := s.ListSources(ctx, SourceFilter{RepoID: "fixture-a", Status: "open", Labels: []string{"bug"}, TitleContains: "Bootstrap", Limit: 1})
			if err != nil || len(gotList) != 1 || gotList[0].ID != "ISSUE-041" {
				t.Fatalf("removed label: %v %#v", err, gotList)
			}
			allTitles, err := s.ListSources(ctx, SourceFilter{RepoID: "fixture-a", Status: "open", Labels: []string{"bug"}, TitleContains: "Bootstrap"})
			if err != nil || len(allTitles) != 4 {
				t.Fatalf("body-only title bypass: %v %#v", err, allTitles)
			}
			gotList, err = s.ListSources(ctx, SourceFilter{RepoID: "fixture-a", Labels: []string{"bug", "missing"}, LabelsMatch: "all"})
			if err != nil || len(gotList) != 0 {
				t.Fatal("all-label predicate lost", err)
			}
		})
	}
}
