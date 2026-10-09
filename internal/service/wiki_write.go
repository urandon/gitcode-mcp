package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

// Wiki writes have a durable single-mutation claim. Any attempted or unknown
// outcome is reconciled by exact-path GET on replay, never another POST/PUT.
func (s *Service) executeWikiContentWrite(ctx context.Context, command string, route RepositoryRoute, req WriteCommandRequest, base WriteCommandResult) (WriteCommandResult, error) {
	key, fingerprint := base.IdempotencyKey, base.SourceFingerprint
	wikiPath := gitcode.WikiWritePath(firstNonEmptyString(req.Path, req.Slug, req.ID, req.Title), "")
	if wikiPath == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "path", Message: "a non-empty normalized wiki path is required"}
	}
	lookup, err := audit.LookupIdempotency(ctx, s.store, route.RepoID, key, fingerprint)
	if err != nil {
		return WriteCommandResult{}, err
	}
	if lookup.Conflict {
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_idempotency_conflict", RepoID: route.RepoID, RemoteID: wikiPath, IdempotencyKey: key}
	}
	if lookup.Replay {
		return replayWikiWriteResult(command, req, *lookup.Entry, fingerprint, s.now().UTC()), nil
	}
	transitioner, ok := s.store.(auditGenerationClaimStore)
	if !ok {
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_audit_start_failed", RepoID: route.RepoID, IdempotencyKey: key}
	}
	var claim cache.AuditTrailEntry
	var confirmation writeConfirmation
	var graph cache.RecordGraph
	recovered := lookup.Entry != nil
	if recovered {
		claim = *lookup.Entry
		// Even legacy failed receipts may follow an applied write with a decode
		// error. Preserve the key and reconcile rather than assume rejection.
		page, readErr := s.client.GetWikiPage(ctx, gitcode.WikiPageRequest{Owner: route.Owner, Repo: route.Name, Slug: wikiPath})
		if readErr != nil {
			return WriteCommandResult{}, wikiRecoveredFailure(claim, wikiPath, "write_ambiguous_readback_failed", "readback", readErr)
		}
		if strings.TrimSpace(page.Revision) == "" || firstNonEmptyString(page.Slug, page.ID) != wikiPath || page.Body != req.Body {
			return WriteCommandResult{}, wikiRecoveredFailure(claim, wikiPath, "write_ambiguous_remote", "readback_mismatch", nil)
		}
		result := gitcode.WriteResult[gitcode.WikiPage]{Record: page, Confirmed: true, Operation: command, RemoteID: wikiPath, RemoteSlug: wikiPath, RemoteRevision: page.Revision, ConfirmedAt: s.now().UTC()}
		confirmation, graph = s.wikiWriteGraph(route.RepoID, page, result, s.now().UTC())
	} else {
		metadata := map[string]string{"wiki_path": wikiPath, "wiki_body_hash": hashWriteInvariant(req.Body), "write_phase": "claimed", "mutation_attempted": "unknown"}
		claim = audit.WithRequestMetadata(audit.InProgress(route.RepoID, key, command, fallbackSourceID("wiki", wikiPath), "wiki", wikiPath, fingerprint, "wiki write pending exact-path confirmation", s.now().UTC()), metadata)
		claimed, claimErr := transitioner.ClaimAuditEventGeneration(ctx, claim, nil)
		if claimErr != nil {
			return WriteCommandResult{}, ErrWriteFailure{Code: "write_audit_start_failed", RepoID: route.RepoID, RemoteID: wikiPath, IdempotencyKey: key}
		}
		if !claimed {
			return WriteCommandResult{}, ErrWriteFailure{Code: "write_idempotency_in_progress", RepoID: route.RepoID, RemoteID: wikiPath, IdempotencyKey: key}
		}
		confirmation, graph, err = s.callWriteAdapter(ctx, command, route, req, key)
		if err != nil || !confirmation.confirmed || confirmation.remoteID != wikiPath {
			phase, attempted := "confirmation", true
			var mutation gitcode.ErrWriteMutationPhase
			if errors.As(err, &mutation) {
				phase, attempted = mutation.Phase, mutation.MutationAttempted
			}
			metadata = cloneStringMap(claim.RequestMetadata)
			metadata["write_phase"] = phase
			metadata["mutation_attempted"] = "true"
			code := "write_ambiguous_remote"
			if !attempted {
				metadata["mutation_attempted"] = "false"
				code = s.writeAdapterErrorCode(req.Mode, err)
			}
			uncertain := audit.WithRequestMetadata(audit.InProgress(route.RepoID, key, command, claim.RecordID, "wiki", wikiPath, fingerprint, code, s.now().UTC()), metadata)
			_, current, transitionErr := s.transitionAuditGeneration(ctx, transitioner, uncertain, claim.CreatedAt, audit.StatusInProgress)
			if auditGenerationSucceeded(current, claim.CreatedAt, fingerprint) {
				return replayWriteResult(command, req, *current, fingerprint, s.now().UTC()), nil
			}
			if transitionErr != nil {
				return WriteCommandResult{}, wikiReconciliationFailure(route.RepoID, wikiPath, key, "write_audit_start_failed", phase, attempted, nil)
			}
			return WriteCommandResult{}, wikiReconciliationFailure(route.RepoID, wikiPath, key, code, phase, attempted, err)
		}
	}

	metadata := cloneStringMap(claim.RequestMetadata)
	for name, value := range writeAuditMetadata(command, key, fingerprint, "wiki", confirmation) {
		metadata[name] = value
	}
	metadata["wiki_path"], metadata["write_phase"] = wikiPath, "canonical_readback_confirmed"
	if !recovered {
		metadata["mutation_attempted"] = "true"
	} else if metadata["mutation_attempted"] == "" {
		metadata["mutation_attempted"] = "unknown"
	}
	metadata["wiki_revision"] = confirmation.remoteRevision
	pending := audit.WithRequestMetadata(audit.RemoteConfirmedCacheRefreshPending(route.RepoID, key, command, graph.Record.ID, "wiki", wikiPath, fingerprint, "wiki exact path and body confirmed; cache refresh pending", s.now().UTC()), metadata)
	settling, current, err := s.transitionAuditGeneration(ctx, transitioner, pending, claim.CreatedAt, claim.Status, audit.StatusRemoteConfirmedCacheRefreshFailed)
	if err != nil || !settling {
		if auditGenerationSucceeded(current, claim.CreatedAt, fingerprint) {
			return replayWriteResult(command, req, *current, fingerprint, s.now().UTC()), nil
		}
		return WriteCommandResult{}, wikiReconciliationFailure(route.RepoID, wikiPath, key, "write_partial_remote_confirmed_audit_failed", "audit", true, nil)
	}
	if err := s.store.UpsertRecordGraph(ctx, graph); err != nil {
		partial := audit.WithRequestMetadata(audit.RemoteConfirmedCacheRefreshFailed(route.RepoID, key, command, graph.Record.ID, "wiki", wikiPath, fingerprint, "wiki cache refresh failed", s.now().UTC()), metadata)
		_, _, _ = s.transitionAuditGeneration(ctx, transitioner, partial, claim.CreatedAt, audit.StatusRemoteConfirmedCacheRefreshPending)
		return WriteCommandResult{}, wikiReconciliationFailure(route.RepoID, wikiPath, key, "write_partial_cache_refresh_failed", "cache_refresh", true, nil)
	}
	complete := audit.WithRequestMetadata(audit.Success(route.RepoID, key, command, graph.Record.ID, "wiki", wikiPath, fingerprint, "canonical wiki readback confirmed", s.now().UTC()), metadata)
	settled, current, err := s.transitionAuditGeneration(ctx, transitioner, complete, claim.CreatedAt, audit.StatusRemoteConfirmedCacheRefreshPending)
	if err != nil || !settled {
		if auditGenerationSucceeded(current, claim.CreatedAt, fingerprint) {
			return replayWriteResult(command, req, *current, fingerprint, s.now().UTC()), nil
		}
		return WriteCommandResult{}, wikiReconciliationFailure(route.RepoID, wikiPath, key, "write_partial_remote_confirmed_audit_failed", "audit", true, nil)
	}
	base.Status, base.ID, base.RemoteID, base.RemoteSlug, base.RemoteRevision = "succeeded", graph.Record.ID, wikiPath, wikiPath, confirmation.remoteRevision
	base.APIPath, base.CachePath, base.BrowserURL = confirmation.apiPath, confirmation.cachePath, confirmation.browserURL
	base.Evidence = "canonical exact-path/body confirmation with durable audit and cache refresh"
	if recovered {
		base.Status, base.Replayed = "recovered_after_ambiguous_write", true
		base.Evidence = "exact-path/body readback recovered the claimed wiki write; no second POST/PUT issued"
	}
	return base, nil
}

func wikiReconciliationFailure(repoID, wikiPath, key, code, phase string, attempted bool, cause error) ErrWriteFailure {
	providerClass := ""
	if cause != nil {
		providerClass = writeErrorCode(cause)
		var missing gitcode.ErrRemoteNotFound
		if errors.As(cause, &missing) {
			providerClass = "wiki_unavailable"
		}
	}
	return ErrWriteFailure{Code: code, RepoID: repoID, RemoteID: wikiPath, IdempotencyKey: key, Cause: cause, ProviderFailureClass: providerClass, WritePhase: phase, MutationAttempted: &attempted, Reconciliation: "reuse the same idempotency key for exact-path GET-only reconciliation; do not issue a new mutation blindly"}
}

func replayWikiWriteResult(command string, req WriteCommandRequest, entry cache.AuditTrailEntry, fingerprint string, now time.Time) WriteCommandResult {
	result := replayWriteResult(command, req, entry, fingerprint, now)
	result.RemoteSlug, result.RemoteRevision = entry.RemoteID, entry.RequestMetadata["wiki_revision"]
	result.CachePath = normalizeWikiCachePath(entry.RemoteID)
	return result
}

func wikiRecoveredFailure(claim cache.AuditTrailEntry, wikiPath, code, phase string, cause error) ErrWriteFailure {
	failure := wikiReconciliationFailure(claim.RepoID, wikiPath, claim.IdempotencyKey, code, phase, true, cause)
	switch claim.RequestMetadata["mutation_attempted"] {
	case "true":
	case "false":
		attempted := false
		failure.MutationAttempted = &attempted
	default:
		// A claim can survive a crash before transport. Readback establishes
		// current state, not whether a historical mutation was attempted.
		failure.MutationAttempted = nil
	}
	return failure
}
