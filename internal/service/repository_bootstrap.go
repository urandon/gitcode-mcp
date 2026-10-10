package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

type RepositoryMetadataResult struct {
	RepoID string `json:"repo_id"`
	gitcode.RepositoryMetadata
	Evidence    string    `json:"evidence"`
	GeneratedAt time.Time `json:"generated_at"`
}

type RepositoryLabelsResult struct {
	RepoID string `json:"repo_id"`
	gitcode.RepositoryLabels
	Evidence    string    `json:"evidence"`
	GeneratedAt time.Time `json:"generated_at"`
}

func (s *Service) bootstrapClient(ctx context.Context, repoID string) (RepositoryRoute, gitcode.RepositoryBootstrapClient, error) {
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return route, nil, err
	}
	client, ok := s.client.(gitcode.RepositoryBootstrapClient)
	if !ok {
		return route, nil, gitcode.ErrProviderUnavailable{Reason: "repository bootstrap capability is unavailable"}
	}
	return route, client, nil
}

// Explicit live observation: local RepositoryStatus is never used as proof of
// visibility. This read neither changes a binding nor authorizes a later write.
func (s *Service) GetRepositoryMetadata(ctx context.Context, repoID string) (RepositoryMetadataResult, error) {
	route, client, err := s.bootstrapClient(ctx, repoID)
	if err != nil {
		return RepositoryMetadataResult{}, err
	}
	meta, err := client.GetRepositoryMetadata(ctx, gitcode.RepoRequest{Owner: route.Owner, Repo: route.Name})
	if err != nil {
		return RepositoryMetadataResult{}, err
	}
	return RepositoryMetadataResult{RepoID: route.RepoID, RepositoryMetadata: meta, Evidence: "explicit adapter-confirmed live observation; not a binding or authorization lease", GeneratedAt: s.now().UTC()}, nil
}

func (s *Service) ListRepositoryLabels(ctx context.Context, repoID string) (RepositoryLabelsResult, error) {
	route, client, err := s.bootstrapClient(ctx, repoID)
	if err != nil {
		return RepositoryLabelsResult{}, err
	}
	list, err := client.ListRepositoryLabels(ctx, gitcode.RepoRequest{Owner: route.Owner, Repo: route.Name})
	if err != nil {
		return RepositoryLabelsResult{}, err
	}
	for _, label := range list.Labels {
		if err := s.store.UpsertRecordGraph(ctx, repositoryLabelGraph(route.RepoID, label, s.now().UTC())); err != nil {
			return RepositoryLabelsResult{}, err
		}
	}
	return RepositoryLabelsResult{RepoID: route.RepoID, RepositoryLabels: list, Evidence: "complete bounded adapter read with label cache refresh; no issue assignment", GeneratedAt: s.now().UTC()}, nil
}

func (s *Service) CreateRepositoryLabel(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Description) != "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "description", Message: "repository label description writes are not qualified; name and color only"}
	}
	label := gitcode.RepositoryLabelRequest{Name: req.Label, Color: req.Color}
	if err := gitcode.ValidateRepositoryLabel(label); err != nil {
		return WriteCommandResult{}, err
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "idempotency_key", Message: "caller key is required; preserve it for reconciliation"}
	}
	req.Color, _ = gitcode.NormalizeRepositoryLabelColor(req.Color)
	route, err := s.BuildAdapterRoute(ctx, firstNonEmptyString(req.RepoID, req.Repo), RepositoryScopeIssues)
	if err != nil {
		return WriteCommandResult{}, err
	}
	req.RepoID, req.Repo = route.RepoID, route.RepoID
	// Only this new command adds color to its fingerprint. Existing write
	// fingerprints/settled receipts remain byte-for-byte compatible.
	payload, _ := json.Marshal(struct{ Command, Repo, Name, Color string }{"create-repo-label", firstNonEmptyString(req.RepoID, req.Repo), req.Label, req.Color})
	req.idempotencyFingerprint = hashWriteInvariant(string(payload))
	return s.executeWrite(ctx, "create-repo-label", req, RepositoryScopeIssues)
}

func repositoryLabelGraph(repoID string, label gitcode.RepositoryLabel, now time.Time) cache.RecordGraph {
	stableID := "LABEL-" + label.ID.String()
	body := "Color: " + label.Color + "\n\n" + label.Description
	revision := contentHash(label.Name, body, label.RepositoryID.String())
	record := cache.Record{RepoID: repoID, ID: stableID, Type: "label", Path: "labels/" + label.ID.String() + ".md", Title: label.Name, Body: body, Status: "current", ContentHash: revision, Provenance: cache.ProvenanceRemote, RemoteType: "label", RemoteID: label.ID.String(), RemoteRevision: revision, CreatedAt: now, UpdatedAt: now}
	return cache.RecordGraph{Record: record, SourceProvenance: cache.ProvenanceLive, Identities: []cache.Identity{{RepoID: repoID, SourceID: stableID, AliasType: "label", Alias: "label:" + label.ID.String(), Remote: cache.RemoteAlias{Type: "label", ID: label.ID.String()}}}, RemoteRevisions: []cache.RemoteRevision{{RepoID: repoID, RecordID: stableID, RemoteType: "label", RemoteID: label.ID.String(), RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}}
}

func replayRepositoryLabelResult(req WriteCommandRequest, entry cache.AuditTrailEntry, fingerprint string, now time.Time) WriteCommandResult {
	result := replayWriteResult("create-repo-label", req, entry, fingerprint, now)
	result.RemoteRevision = entry.RequestMetadata["label_revision"]
	return result
}

// One durable claim fences the POST. Every later call with that key is a
// canonical list-only reconciliation, including provider/cache/audit failures.
func (s *Service) executeRepositoryLabelWrite(ctx context.Context, route RepositoryRoute, req WriteCommandRequest, base WriteCommandResult) (WriteCommandResult, error) {
	const command = "create-repo-label"
	key, fingerprint := base.IdempotencyKey, base.SourceFingerprint
	failure := func(code string, cause error) (WriteCommandResult, error) {
		providerClass := ""
		if cause != nil {
			providerClass = writeErrorCode(cause)
		}
		return WriteCommandResult{}, ErrWriteFailure{Code: code, RepoID: route.RepoID, IdempotencyKey: key, Cause: cause, ProviderFailureClass: providerClass, Reconciliation: "keep the original key and arguments for complete canonical GET-only reconciliation; do not blindly repeat POST"}
	}
	lookup, err := audit.LookupIdempotency(ctx, s.store, route.RepoID, key, fingerprint)
	if err != nil {
		return WriteCommandResult{}, err
	}
	if lookup.Conflict {
		return failure("write_idempotency_conflict", nil)
	}
	if lookup.Replay {
		return replayRepositoryLabelResult(req, *lookup.Entry, fingerprint, s.now().UTC()), nil
	}
	client, ok := s.client.(gitcode.RepositoryBootstrapClient)
	if !ok {
		return failure("write_provider_unavailable", nil)
	}
	claimer, ok := s.store.(auditGenerationClaimStore)
	if !ok {
		return failure("write_audit_start_failed", nil)
	}
	settler, ok := s.store.(wikiWriteSettlementStore)
	if !ok {
		return failure("write_audit_start_failed", nil)
	}
	list, err := client.ListRepositoryLabels(ctx, gitcode.RepoRequest{Owner: route.Owner, Repo: route.Name})
	if err != nil {
		return failure("write_unconfirmed_remote", err)
	}
	var found *gitcode.RepositoryLabel
	for i := range list.Labels {
		if list.Labels[i].Name == req.Label {
			found = &list.Labels[i]
		}
	}
	var claim cache.AuditTrailEntry
	var label gitcode.RepositoryLabel
	recovered := lookup.Entry != nil
	if recovered {
		claim = *lookup.Entry
		if found == nil || found.Color != req.Color || found.RepositoryID.String() != list.RepositoryID || (claim.RemoteID != "" && claim.RemoteID != found.ID.String()) {
			return failure("write_ambiguous_remote", nil)
		}
		label = *found
	} else {
		if found != nil {
			return failure("write_conflict", nil)
		}
		claim = audit.WithRequestMetadata(audit.InProgress(route.RepoID, key, command, "LABEL-PENDING", "label", "", fingerprint, "label creation pending canonical confirmation", s.now().UTC()), map[string]string{"method": "POST", "label_repository_id": list.RepositoryID})
		claimed, err := claimer.ClaimAuditEventGeneration(ctx, claim, nil)
		if err != nil {
			return failure("write_audit_start_failed", err)
		}
		if !claimed {
			return failure("write_idempotency_in_progress", nil)
		}
		result, err := client.CreateRepositoryLabel(ctx, gitcode.RepositoryLabelRequest{Owner: route.Owner, Repo: route.Name, Name: req.Label, Color: req.Color}, gitcode.WriteOptions{IdempotencyKey: key})
		labelID, _ := result.Record.ID.Int64()
		if err != nil || !result.Confirmed || result.Record.RepositoryID.String() != list.RepositoryID || result.Record.Name != req.Label || result.Record.Color != req.Color || labelID <= 0 || result.RemoteID != result.Record.ID.String() {
			// Preserve any canonical identity from the acknowledgement even when
			// readback failed. Later exact-name reconciliation must retain it.
			uncertain := claim
			var identity gitcode.ErrRepositoryLabelWrite
			if errors.As(err, &identity) {
				uncertain.RemoteID = identity.RemoteID
			}
			if uncertain.RemoteID != "" {
				_, current, transitionErr := s.transitionAuditGeneration(ctx, claimer, uncertain, claim.CreatedAt, audit.StatusInProgress)
				if auditGenerationSucceeded(current, claim.CreatedAt, fingerprint) {
					return replayRepositoryLabelResult(req, *current, fingerprint, s.now().UTC()), nil
				}
				if transitionErr != nil {
					return failure("write_audit_start_failed", nil)
				}
			}
			return failure("write_ambiguous_remote", err)
		}
		label = result.Record
	}
	if claim.RequestMetadata["label_repository_id"] != list.RepositoryID {
		return failure("write_ambiguous_remote", nil)
	}
	graph := repositoryLabelGraph(route.RepoID, label, s.now().UTC())
	metadata := cloneStringMap(claim.RequestMetadata)
	metadata["label_revision"] = graph.Record.RemoteRevision
	pending := audit.WithRequestMetadata(audit.RemoteConfirmedCacheRefreshPending(route.RepoID, key, command, graph.Record.ID, "label", label.ID.String(), fingerprint, "canonical label confirmed; publication pending", s.now().UTC()), metadata)
	currentResult := func() (WriteCommandResult, bool) {
		current, _ := s.store.GetAuditEventByKey(ctx, route.RepoID, key)
		if auditGenerationSucceeded(current, claim.CreatedAt, fingerprint) {
			return replayRepositoryLabelResult(req, *current, fingerprint, s.now().UTC()), true
		}
		return WriteCommandResult{}, false
	}
	staged, err := settler.StageWriteGraphGeneration(ctx, pending, claim, claim.CreatedAt)
	if err != nil || !staged {
		if result, ok := currentResult(); ok {
			return result, nil
		}
		return failure("write_partial_remote_confirmed_audit_failed", err)
	}
	complete := audit.WithRequestMetadata(audit.Success(route.RepoID, key, command, graph.Record.ID, "label", label.ID.String(), fingerprint, "exact name/color/repository confirmed without issue assignment", s.now().UTC()), metadata)
	settled, err := settler.SettleWriteGraphGeneration(ctx, graph, complete, pending, claim.CreatedAt)
	if err != nil || !settled {
		if result, ok := currentResult(); ok {
			return result, nil
		}
		return failure("write_partial_cache_refresh_failed", err)
	}
	base.Status, base.ID, base.RemoteID, base.RemoteRevision = "succeeded", graph.Record.ID, label.ID.String(), graph.Record.RemoteRevision
	base.Evidence = "canonical standalone label readback with atomic audit/cache publication; no issue assignment"
	if recovered {
		base.Status, base.Replayed = "recovered_after_ambiguous_write", true
	}
	return base, nil
}
