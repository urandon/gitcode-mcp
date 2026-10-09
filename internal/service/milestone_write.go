package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

func milestoneWriteFailure(claim cache.AuditTrailEntry, code, phase string, cause error) ErrWriteFailure {
	f := ErrWriteFailure{Code: code, RepoID: claim.RepoID, RemoteID: claim.RemoteID, IdempotencyKey: claim.IdempotencyKey, WritePhase: phase, Cause: cause, Reconciliation: "keep the original idempotency key; reconcile the canonical milestone by GET/list, never repeat an uncertain mutation with a new key"}
	if cause != nil {
		f.ProviderFailureClass = writeErrorCode(cause)
	}
	switch claim.RequestMetadata["mutation_attempted"] {
	case "true":
		v := true
		f.MutationAttempted = &v
	case "false":
		v := false
		f.MutationAttempted = &v
	}
	return f
}

func replayMilestoneWrite(command string, req WriteCommandRequest, e cache.AuditTrailEntry, fingerprint string, now time.Time) WriteCommandResult {
	r := replayWriteResult(command, req, e, fingerprint, now)
	r.RemoteRevision = e.RequestMetadata["milestone_revision"]
	return r
}

// Milestone confirmation failures must not turn strict readback into a duplicate
// POST/PATCH. Unknown create identities remain fenced for manual reconciliation.
func (s *Service) executeMilestoneWrite(ctx context.Context, command string, route RepositoryRoute, req WriteCommandRequest, base WriteCommandResult) (WriteCommandResult, error) {
	key, fingerprint := base.IdempotencyKey, base.SourceFingerprint
	lookup, err := audit.LookupIdempotency(ctx, s.store, route.RepoID, key, fingerprint)
	if err != nil {
		return WriteCommandResult{}, err
	}
	if lookup.Conflict {
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_idempotency_conflict", RepoID: route.RepoID, IdempotencyKey: key}
	}
	if lookup.Replay {
		return replayMilestoneWrite(command, req, *lookup.Entry, fingerprint, s.now().UTC()), nil
	}
	claimer, ok := s.store.(auditGenerationClaimStore)
	if !ok {
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_audit_start_failed", RepoID: route.RepoID, IdempotencyKey: key}
	}
	settler, ok := s.store.(wikiWriteSettlementStore)
	if !ok {
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_audit_start_failed", RepoID: route.RepoID, IdempotencyKey: key}
	}
	var claim cache.AuditTrailEntry
	var confirmed writeConfirmation
	var graph cache.RecordGraph
	recovered := lookup.Entry != nil
	if recovered {
		claim = *lookup.Entry
		id, parseErr := strconv.Atoi(claim.RemoteID)
		if parseErr != nil || id <= 0 {
			return WriteCommandResult{}, milestoneWriteFailure(claim, "write_ambiguous_remote", "identity_unknown", nil)
		}
		m, readErr := s.client.GetMilestone(ctx, gitcode.MilestoneRequest{Owner: route.Owner, Repo: route.Name, ID: id})
		if readErr != nil {
			return WriteCommandResult{}, milestoneWriteFailure(claim, "write_ambiguous_readback_failed", "readback", readErr)
		}
		if m.RemoteID != claim.RemoteID {
			return WriteCommandResult{}, milestoneWriteFailure(claim, "write_ambiguous_remote", "readback_mismatch", nil)
		}
		if err := gitcode.ConfirmMilestoneFields(gitcode.MilestoneWriteRequest{Title: req.Title, Description: req.Description, DueOn: req.DueOn, State: req.State}, m, command == "create-milestone"); err != nil {
			return WriteCommandResult{}, milestoneWriteFailure(claim, "write_ambiguous_remote", "readback_mismatch", err)
		}
		result := gitcode.WriteResult[gitcode.Milestone]{Record: m, Confirmed: true, RemoteID: m.RemoteID, RemoteRevision: m.UpdatedAt, ConfirmedAt: s.now().UTC()}
		confirmed, graph = s.milestoneWriteGraph(route.RepoID, m, result, s.now().UTC())
	} else {
		remoteID, method := "", "POST"
		if command == "update-milestone" {
			id, resolveErr := s.resolveMilestoneID(ctx, route, firstNonEmptyString(req.Milestone, req.ID))
			if resolveErr != nil {
				return WriteCommandResult{}, resolveErr
			}
			remoteID, method = strconv.Itoa(id), "PATCH"
			req.Milestone = remoteID
		}
		claim = audit.WithRequestMetadata(audit.InProgress(route.RepoID, key, command, fallbackSourceID("milestone", firstNonEmptyString(remoteID, fingerprint)), "milestone", remoteID, fingerprint, "milestone mutation pending canonical confirmation", s.now().UTC()), map[string]string{"method": method, "write_phase": "claimed", "mutation_attempted": "unknown"})
		claimed, claimErr := claimer.ClaimAuditEventGeneration(ctx, claim, nil)
		if claimErr != nil {
			return WriteCommandResult{}, milestoneWriteFailure(claim, "write_audit_start_failed", "audit", claimErr)
		}
		if !claimed {
			return WriteCommandResult{}, milestoneWriteFailure(claim, "write_idempotency_in_progress", "claimed", nil)
		}
		confirmed, graph, err = s.callWriteAdapter(ctx, command, route, req, key)
		if err != nil || !confirmed.confirmed || confirmed.remoteID == "" {
			uncertain := claim
			var identity gitcode.ErrMilestoneWrite
			if errors.As(err, &identity) && identity.RemoteID != "" {
				uncertain.RemoteID = identity.RemoteID
			}
			uncertain.RequestMetadata = cloneStringMap(claim.RequestMetadata)
			phase, attempted := "confirmation", true
			var mutation gitcode.ErrWriteMutationPhase
			if errors.As(err, &mutation) {
				phase, attempted = mutation.Phase, mutation.MutationAttempted
			}
			uncertain.RequestMetadata["write_phase"] = phase
			uncertain.RequestMetadata["mutation_attempted"] = strconv.FormatBool(attempted)
			_, current, transitionErr := s.transitionAuditGeneration(ctx, claimer, uncertain, claim.CreatedAt, audit.StatusInProgress)
			if auditGenerationSucceeded(current, claim.CreatedAt, fingerprint) {
				return replayMilestoneWrite(command, req, *current, fingerprint, s.now().UTC()), nil
			}
			if transitionErr != nil {
				return WriteCommandResult{}, milestoneWriteFailure(uncertain, "write_audit_start_failed", "audit", transitionErr)
			}
			return WriteCommandResult{}, milestoneWriteFailure(uncertain, "write_ambiguous_remote", phase, err)
		}
	}
	metadata := cloneStringMap(claim.RequestMetadata)
	metadata["write_phase"], metadata["milestone_revision"] = "canonical_readback_confirmed", confirmed.remoteRevision
	if !recovered {
		metadata["mutation_attempted"] = "true"
	}
	pending := audit.WithRequestMetadata(audit.RemoteConfirmedCacheRefreshPending(route.RepoID, key, command, graph.Record.ID, "milestone", confirmed.remoteID, fingerprint, "milestone confirmed; cache publication pending", s.now().UTC()), metadata)
	staged, err := settler.StageWriteGraphGeneration(ctx, pending, claim, claim.CreatedAt)
	if err != nil || !staged {
		current, _ := s.store.GetAuditEventByKey(ctx, route.RepoID, key)
		if auditGenerationSucceeded(current, claim.CreatedAt, fingerprint) {
			return replayMilestoneWrite(command, req, *current, fingerprint, s.now().UTC()), nil
		}
		return WriteCommandResult{}, milestoneWriteFailure(claim, "write_partial_remote_confirmed_audit_failed", "audit", nil)
	}
	complete := audit.WithRequestMetadata(audit.Success(route.RepoID, key, command, graph.Record.ID, "milestone", confirmed.remoteID, fingerprint, "canonical milestone fields confirmed", s.now().UTC()), metadata)
	settled, err := settler.SettleWriteGraphGeneration(ctx, graph, complete, pending, claim.CreatedAt)
	if err != nil || !settled {
		current, _ := s.store.GetAuditEventByKey(ctx, route.RepoID, key)
		if auditGenerationSucceeded(current, claim.CreatedAt, fingerprint) {
			return replayMilestoneWrite(command, req, *current, fingerprint, s.now().UTC()), nil
		}
		return WriteCommandResult{}, milestoneWriteFailure(pending, "write_partial_cache_refresh_failed", "cache_refresh", nil)
	}
	base.Status, base.ID, base.RemoteID, base.RemoteNumber, base.RemoteRevision = "succeeded", graph.Record.ID, confirmed.remoteID, confirmed.remoteNumber, confirmed.remoteRevision
	base.BrowserURL = confirmed.browserURL
	base.Evidence = "canonical milestone readback with atomic audit/cache publication"
	if recovered {
		base.Status, base.Replayed = "recovered_after_ambiguous_write", true
	}
	return base, nil
}
