package service

import "context"

// SyncSelection is the effective, ordered collection intent, independent of
// foreground traversal limits or durable stage budgets. Legacy comments aliases
// are resolved by their existing transport contract before selection.
type SyncSelection struct {
	Issues, IssueComments, Wiki, Pulls, PRComments bool
}

func (s SyncSelection) Collections() []string {
	out := []string{}
	for _, item := range []struct {
		selected bool
		name     string
	}{
		{s.Issues, "issues"}, {s.IssueComments, "issue_comments"},
		{s.Wiki, "wiki"}, {s.Pulls, "pulls"}, {s.PRComments, "pr_comments"},
	} {
		if item.selected {
			out = append(out, item.name)
		}
	}
	return out
}

// SelectSyncCollections resolves omission against existing binding scopes and
// rejects the entire explicit request when any requested scope is unavailable.
func SelectSyncCollections(scopes []RepositoryScope, requested SyncSelection) (SyncSelection, error) {
	issues, wiki := false, false
	for _, scope := range scopes {
		issues = issues || scope == RepositoryScopeIssues
		wiki = wiki || scope == RepositoryScopeWiki
	}
	if len(requested.Collections()) == 0 {
		if !issues && !wiki {
			return SyncSelection{}, ErrInvalidQuery{Field: "scope", Message: "no eligible default sync scope is enabled"}
		}
		return SyncSelection{Issues: issues, Wiki: wiki}, nil
	}
	if (!issues && (requested.Issues || requested.IssueComments || requested.Pulls || requested.PRComments)) || (!wiki && requested.Wiki) {
		return SyncSelection{}, ErrInvalidQuery{Field: "scope", Message: "a requested sync collection scope is not enabled"}
	}
	return requested, nil
}

func (s *Service) ResolveSyncSelection(ctx context.Context, repoID string, requested SyncSelection) (SyncSelection, error) {
	repoID, err := s.requireRepo(ctx, repoID, "sync selection")
	if err != nil {
		return SyncSelection{}, err
	}
	binding, err := s.store.GetRepository(ctx, repoID)
	if err != nil {
		return SyncSelection{}, normalizeError(err, "repository", repoID)
	}
	scopes := make([]RepositoryScope, len(binding.Scopes))
	for i, scope := range binding.Scopes {
		scopes[i] = RepositoryScope(scope)
	}
	return SelectSyncCollections(scopes, requested)
}
