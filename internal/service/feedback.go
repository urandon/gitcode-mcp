package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gitcode-mcp/internal/buildinfo"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/feedback"
)

type SubmitFeedbackRequest struct {
	Draft          feedback.Draft `json:"draft"`
	Mode           WriteMode      `json:"write_mode"`
	IdempotencyKey string         `json:"idempotency_key"`
}

type FeedbackSink interface {
	Name() string
	Submit(context.Context, feedback.PreparedReport, string) (feedback.SubmissionResult, error)
}

type gitCodeIssueFeedbackSink struct {
	service *Service
	config  feedback.Config
}

func (s gitCodeIssueFeedbackSink) Name() string { return feedback.SinkGitCodeIssues }

func (s gitCodeIssueFeedbackSink) Submit(ctx context.Context, prepared feedback.PreparedReport, idempotencyKey string) (feedback.SubmissionResult, error) {
	write, err := s.service.CreateIssue(ctx, WriteCommandRequest{
		RepoID:                 s.config.RepoID,
		Repo:                   s.config.RepoID,
		Mode:                   WriteModeLive,
		Title:                  prepared.Title,
		Body:                   prepared.Body,
		Labels:                 append([]string(nil), s.config.Labels...),
		IdempotencyKey:         idempotencyKey,
		idempotencyFingerprint: prepared.Fingerprint,
	})
	if err != nil {
		return feedback.SubmissionResult{}, err
	}
	number := write.RemoteNumber
	if number == 0 {
		number = feedback.ParseIssueNumber(write.ID)
	}
	url := strings.TrimSpace(write.BrowserURL)
	if url == "" && number > 0 {
		url = fmt.Sprintf("https://gitcode.com/%s/issues/%d", s.config.RepoID, number)
	}
	return feedback.SubmissionResult{
		Status:         "submitted",
		Sink:           s.Name(),
		RepoID:         s.config.RepoID,
		TicketID:       write.ID,
		TicketNumber:   number,
		TicketURL:      url,
		Fingerprint:    prepared.Fingerprint,
		DedupeDecision: prepared.DedupeDecision,
		IdempotencyKey: write.IdempotencyKey,
		Replayed:       write.Replayed,
		Evidence:       firstNonEmptyString(write.Evidence, "adapter-confirmed issue write with sanitized readback and audit evidence"),
		GeneratedAt:    write.GeneratedAt,
	}, nil
}

func normalizeFeedbackConfig(cfg feedback.Config) feedback.Config {
	if normalized, err := feedback.NormalizeConfig(cfg); err == nil {
		return normalized
	}
	return feedback.DefaultConfig()
}

func (s *Service) ConfigureFeedback(cfg feedback.Config) {
	s.feedbackConfig = normalizeFeedbackConfig(cfg)
}

func (s *Service) PrepareFeedback(ctx context.Context, draft feedback.Draft) (feedback.PreparedReport, error) {
	if err := ctx.Err(); err != nil {
		return feedback.PreparedReport{}, err
	}
	context := s.feedbackRuntimeContext(ctx)
	existing, err := s.feedbackExistingIssues(ctx)
	if err != nil {
		return feedback.PreparedReport{}, err
	}
	prepared, err := feedback.Prepare(draft, context, s.feedbackConfig, existing)
	if err != nil {
		return feedback.PreparedReport{}, err
	}
	return prepared, nil
}

func (s *Service) SubmitFeedback(ctx context.Context, req SubmitFeedbackRequest) (feedback.SubmissionResult, error) {
	if req.Mode != WriteModeLive {
		return feedback.SubmissionResult{}, ErrInvalidQuery{Field: "write_mode", Message: "submit_feedback requires write_mode=live"}
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		return feedback.SubmissionResult{}, ErrInvalidQuery{Field: "idempotency_key", Message: "is required for feedback submission"}
	}
	prepared, err := s.PrepareFeedback(ctx, req.Draft)
	if err != nil {
		return feedback.SubmissionResult{}, err
	}
	base := feedback.SubmissionResult{Status: prepared.Status, Sink: prepared.Sink, RepoID: prepared.RepoID, Fingerprint: prepared.Fingerprint, DedupeDecision: prepared.DedupeDecision, Candidates: prepared.Candidates, IdempotencyKey: key, Remediation: prepared.Remediation, GeneratedAt: s.now().UTC()}
	if !prepared.Configured {
		return base, nil
	}
	if prepared.Status == "duplicate" && len(prepared.Candidates) > 0 {
		candidate := prepared.Candidates[0]
		base.Status = "duplicate"
		base.TicketID = candidate.ID
		base.TicketNumber = candidate.Number
		base.TicketURL = candidate.URL
		if prepared.DedupeDecision == "exact_match" {
			base.Evidence = "existing open feedback issue matched the deterministic fingerprint; no write performed"
		} else {
			base.Evidence = "duplicate_policy=return_existing selected the strongest cached likely match; no write performed"
		}
		return base, nil
	}
	if prepared.Status == "duplicate_candidates" {
		base.Evidence = "likely duplicates found in the configured feedback cache; no write performed"
		return base, nil
	}
	sink, err := s.feedbackSink()
	if err != nil {
		return feedback.SubmissionResult{}, err
	}
	return sink.Submit(ctx, prepared, key)
}

func (s *Service) feedbackSink() (FeedbackSink, error) {
	if !s.feedbackConfig.Enabled || strings.TrimSpace(s.feedbackConfig.RepoID) == "" {
		return nil, ErrInvalidQuery{Field: "feedback", Message: "feedback sink is not configured"}
	}
	switch s.feedbackConfig.Sink {
	case feedback.SinkGitCodeIssues:
		return gitCodeIssueFeedbackSink{service: s, config: s.feedbackConfig}, nil
	default:
		return nil, ErrInvalidQuery{Field: "feedback.sink", Message: "unsupported feedback sink"}
	}
}

func (s *Service) feedbackRuntimeContext(ctx context.Context) feedback.RuntimeContext {
	build := buildinfo.Current()
	result := feedback.DefaultRuntimeContext(s.now().UTC())
	result.Version = build.Version
	result.Commit = build.Commit
	result.ProviderMode = string(s.ProviderMode())
	result.ExpectedSchema = cache.CurrentSchemaVersion()
	if s.store == nil {
		result.SinkBindingState = "cache_unavailable"
		return result
	}
	if version, err := s.store.SchemaVersion(ctx); err == nil {
		result.CacheSchemaVersion = version
		result.SchemaCompatible = version == result.ExpectedSchema
	}
	if strings.TrimSpace(s.feedbackConfig.RepoID) == "" {
		result.SinkBindingState = "not_configured"
	} else if _, err := s.store.GetRepository(ctx, s.feedbackConfig.RepoID); err == nil {
		result.SinkBindingState = "configured"
	} else if isCacheNotFound(err) {
		result.SinkBindingState = "not_bound"
	} else {
		result.SinkBindingState = "unavailable"
	}
	return result
}

func (s *Service) feedbackExistingIssues(ctx context.Context) ([]feedback.ExistingIssue, error) {
	if s.store == nil || strings.TrimSpace(s.feedbackConfig.RepoID) == "" {
		return nil, nil
	}
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{RepoID: s.feedbackConfig.RepoID, Kind: "issue", Status: "open"})
	if err != nil {
		if isCacheNotFound(err) {
			return nil, nil
		}
		return nil, normalizeError(err, "feedback candidates", s.feedbackConfig.RepoID)
	}
	out := make([]feedback.ExistingIssue, 0, len(sources))
	for _, source := range sources {
		number := issueNumberFromFeedbackSource(source)
		url := ""
		if number > 0 {
			url = fmt.Sprintf("https://gitcode.com/%s/issues/%d", s.feedbackConfig.RepoID, number)
		}
		out = append(out, feedback.ExistingIssue{ID: source.ID, Number: number, Title: source.Title, Body: source.Body, URL: url, Status: source.Status})
	}
	return out, nil
}

func issueNumberFromFeedbackSource(source cache.Source) int {
	for _, alias := range source.Aliases {
		if alias.Remote.Type == "issue" || alias.AliasType == "issue" {
			value := firstNonEmptyString(alias.Remote.ID, alias.Alias)
			if number, err := strconv.Atoi(value); err == nil && number > 0 {
				return number
			}
		}
	}
	return feedback.ParseIssueNumber(source.ID)
}
