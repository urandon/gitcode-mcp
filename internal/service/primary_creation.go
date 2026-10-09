package service

import (
	"context"
	"encoding/json"
	"strconv"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
)

func isPrimaryCreation(command string) bool {
	return command == "create-issue" || command == "create-pr"
}

// The initial caller and each GET-only repair compete for the exact observed
// receipt. Graph, origin, confirmation, and success publish in one transaction;
// a delayed caller can neither replace newer content nor downgrade its receipt.
func (s *Service) settlePrimaryCreation(ctx context.Context, command string, req WriteCommandRequest, observed cache.AuditTrailEntry, graph cache.RecordGraph, complete cache.AuditTrailEntry, recovered bool) (WriteCommandResult, error) {
	settler, ok := s.store.(wikiWriteSettlementStore)
	failure := func(code string, cause error) (WriteCommandResult, error) {
		return WriteCommandResult{}, ErrWriteFailure{Code: code, RepoID: observed.RepoID, RemoteID: complete.RemoteID, IdempotencyKey: observed.IdempotencyKey, Cause: cause}
	}
	if !ok {
		return failure("write_audit_start_failed", nil)
	}
	metadata := cloneStringMap(complete.RequestMetadata)
	if metadata["provider_id"] == "" || metadata["remote_number"] == "" {
		return failure("write_unconfirmed_remote", nil)
	}
	snapshot, err := json.Marshal(graph)
	if err != nil {
		return failure("write_partial_cache_refresh_failed", err)
	}
	metadata["primary_snapshot_hash"] = hashWriteInvariant(string(snapshot))
	complete = audit.WithRequestMetadata(complete, metadata)
	pending := audit.WithRequestMetadata(audit.RemoteConfirmedCacheRefreshPending(observed.RepoID, observed.IdempotencyKey, command, graph.Record.ID, graph.Record.RemoteType, complete.RemoteID, observed.PayloadHash, "canonical creation confirmed; atomic cache publication pending", s.now().UTC()), metadata)
	currentResult := func() (WriteCommandResult, bool) {
		current, err := s.store.GetAuditEventByKey(ctx, observed.RepoID, observed.IdempotencyKey)
		if err == nil && auditGenerationSucceeded(current, observed.CreatedAt, observed.PayloadHash) {
			return replayWriteResult(command, req, *current, observed.PayloadHash, s.now().UTC()), true
		}
		return WriteCommandResult{}, false
	}
	staged, err := settler.StageWriteGraphGeneration(ctx, pending, observed, observed.CreatedAt)
	if err != nil || !staged {
		if result, ok := currentResult(); ok {
			return result, nil
		}
		return failure("write_partial_remote_confirmed_audit_failed", err)
	}
	if command == "create-issue" {
		graph.CacheConfirmations = append(graph.CacheConfirmations, cache.CacheConfirmationRecord{RepoID: observed.RepoID, Command: command, RecordID: graph.Record.ID, RecordType: graph.Record.Type, RemoteType: graph.Record.RemoteType, RemoteID: complete.RemoteID, IdempotencyKey: observed.IdempotencyKey, Status: "succeeded", SourceFingerprint: observed.PayloadHash, CreatedAt: complete.CreatedAt})
	}
	settled, err := settler.SettleWriteGraphGeneration(ctx, graph, complete, pending, observed.CreatedAt)
	if err != nil || !settled {
		if result, ok := currentResult(); ok {
			return result, nil
		}
		// Retain pending ownership on rollback. Do not issue an unfenced failure
		// update which could downgrade another recovery already in flight.
		return failure("write_partial_cache_refresh_failed", err)
	}
	result := replayWriteResult(command, req, complete, observed.PayloadHash, s.now().UTC())
	result.Status, result.Replayed = "succeeded", recovered
	result.RemoteNumber, _ = strconv.Atoi(metadata["remote_number"])
	result.Evidence = "canonical creation confirmation with atomic audit and cache publication"
	return result, nil
}
