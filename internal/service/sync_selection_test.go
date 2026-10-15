package service

import (
	"errors"
	"reflect"
	"testing"
)

func TestSyncSelectionContract(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scopes    []RepositoryScope
		requested SyncSelection
		want      []string
		invalid   bool
	}{
		{"issues default", []RepositoryScope{RepositoryScopeIssues}, SyncSelection{}, []string{"issues"}, false},
		{"wiki default", []RepositoryScope{RepositoryScopeWiki}, SyncSelection{}, []string{"wiki"}, false},
		{"both default", []RepositoryScope{RepositoryScopeWiki, RepositoryScopeIssues}, SyncSelection{}, []string{"issues", "wiki"}, false},
		{"empty default", nil, SyncSelection{}, nil, true},
		{"unsupported explicit", []RepositoryScope{RepositoryScopeIssues}, SyncSelection{Issues: true, Wiki: true}, nil, true},
		{"wiki cannot grant comments", []RepositoryScope{RepositoryScopeWiki}, SyncSelection{PRComments: true}, nil, true},
		{"explicit issues family", []RepositoryScope{RepositoryScopeIssues}, SyncSelection{Issues: true, Pulls: true, IssueComments: true, PRComments: true}, []string{"issues", "issue_comments", "pulls", "pr_comments"}, false},
		{"explicit subset", []RepositoryScope{RepositoryScopeIssues, RepositoryScopeWiki}, SyncSelection{Wiki: true}, []string{"wiki"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectSyncCollections(tc.scopes, tc.requested)
			if tc.invalid {
				var invalid ErrInvalidQuery
				if !errors.As(err, &invalid) || invalid.Field != "scope" {
					t.Fatal("missing typed whole-selection rejection")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got.Collections(), tc.want) {
				t.Fatal("effective collection intent differs")
			}
		})
	}
}
