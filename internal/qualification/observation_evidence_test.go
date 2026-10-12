package qualification

import (
	"errors"
	"testing"

	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
)

// This fixture assertion does not infer execution success from telemetry.
// Its caller separately joins the actual durable owner and verifies job, cache
// and recovered health. Coverage can explain only a newly lost commit event,
// not invent the missing admission/provider/terminal causal chain.
func qualifyJobEvidence(page observability.Page, baseline observability.Coverage, boot string, refs observability.EntityRefs, outcome, failureClass string) error {
	if page.HasMore || boot == "" || refs.Job == "" || refs.Cache == "" || refs.Repo == "" {
		return errors.New("actual correlated evidence scope is incomplete")
	}
	admitted, terminal, provider, committed := false, false, false, false
	for _, event := range page.Events {
		if event.JobRef != refs.Job {
			continue
		}
		if event.BootID != boot || event.CorrelationRef == "" || event.RepoRef != refs.Repo || event.CacheRef != refs.Cache {
			return errors.New("actual job event lost causal identity")
		}
		admitted = admitted || event.Code == observability.JobAdmitted
		if event.Code == observability.JobTerminal && outcome == servicectl.JobStatusFailed && event.FailureClass != failureClass {
			return errors.New("actual terminal lost its finite provider failure class")
		}
		terminal = terminal || event.Code == observability.JobTerminal && event.Outcome == outcome
		provider = provider || event.Code == observability.ProviderAttempt && event.Phase == "fetch" && (outcome != servicectl.JobStatusSucceeded || event.Outcome == "succeeded")
		committed = committed || event.Code == observability.SyncCommitted && event.Collection == "issues" && event.Outcome == "succeeded"
	}
	if !admitted || !terminal || !provider {
		return errors.New("actual correlated admission/provider/terminal evidence missing")
	}
	if outcome == servicectl.JobStatusSucceeded && !committed {
		coverage := page.Coverage
		if coverage.State != "partial" || coverage.Reason != "observation_contention" || coverage.Dropped <= baseline.Dropped || coverage.LossEpoch <= baseline.LossEpoch {
			return errors.New("actual commit observation missing without new accounted contention loss")
		}
	}
	return nil
}

func TestO5CommitEvidenceRequiresNewAccountedLoss(t *testing.T) {
	refs := observability.EntityRefs{Job: "fixture-job", Cache: "fixture-cache", Repo: "fixture-repo"}
	event := func(code observability.Code) observability.Event {
		return observability.Event{BootID: "fixture-boot", Code: code, JobRef: refs.Job, CacheRef: refs.Cache, RepoRef: refs.Repo, CorrelationRef: "fixture-correlation", ExecutionDetails: observability.ExecutionDetails{Phase: "fetch", Collection: "issues", Outcome: "succeeded"}}
	}
	baseline := observability.Coverage{State: "partial", Reason: "recovered_partial", Dropped: 7, LossEpoch: 7}
	complete := []observability.Event{event(observability.JobAdmitted), event(observability.ProviderAttempt), event(observability.JobTerminal), event(observability.SyncCommitted)}
	for _, tc := range []struct {
		name     string
		coverage observability.Coverage
		edit     func(*observability.Page)
		want     bool
	}{
		{"ready_complete", observability.Coverage{State: "ready"}, nil, true},
		{"ready_missing", observability.Coverage{State: "ready", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events = p.Events[:3] }, false},
		{"unknown_missing", observability.Coverage{Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events = p.Events[:3] }, false},
		{"accounted_missing", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events = p.Events[:3] }, true},
		{"old_loss", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 7, LossEpoch: 7}, func(p *observability.Page) { p.Events = p.Events[:3] }, false},
		{"no_new_drops", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 7, LossEpoch: 8}, func(p *observability.Page) { p.Events = p.Events[:3] }, false},
		{"no_new_epoch", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 8, LossEpoch: 7}, func(p *observability.Page) { p.Events = p.Events[:3] }, false},
		{"storage_loss", observability.Coverage{State: "partial", Reason: "storage_failure", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events = p.Events[:3] }, false},
		{"unconfirmed_boundary", observability.Coverage{State: "partial", Reason: "execution_boundary_unconfirmed", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events = p.Events[:3] }, false},
		{"truncated", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events = p.Events[:3]; p.HasMore = true }, false},
		{"missing_admission", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events = p.Events[1:] }, false},
		{"missing_provider", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events[1].Code = observability.JobAdmitted }, false},
		{"failed_provider_for_success", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events[1].Outcome = "failed" }, false},
		{"missing_terminal", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events[2].Code = observability.JobAdmitted }, false},
		{"wrong_terminal", observability.Coverage{State: "partial", Reason: "observation_contention", Dropped: 8, LossEpoch: 8}, func(p *observability.Page) { p.Events[2].Outcome = "failed" }, false},
		{"sibling_commit", observability.Coverage{State: "ready"}, func(p *observability.Page) { p.Events[3].JobRef = "fixture-sibling" }, false},
		{"prior_boot_commit", observability.Coverage{State: "ready"}, func(p *observability.Page) { p.Events[3].BootID = "fixture-prior-boot" }, false},
		{"wrong_cache_commit", observability.Coverage{State: "ready"}, func(p *observability.Page) { p.Events[3].CacheRef = "fixture-other-cache" }, false},
		{"wrong_collection_commit", observability.Coverage{State: "ready"}, func(p *observability.Page) { p.Events[3].Collection = "wiki" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := observability.Page{Events: append([]observability.Event(nil), complete...), Coverage: tc.coverage}
			if tc.edit != nil {
				tc.edit(&page)
			}
			if err := qualifyJobEvidence(page, baseline, "fixture-boot", refs, servicectl.JobStatusSucceeded, ""); (err == nil) != tc.want {
				t.Fatal("qualification accepted missing evidence or rejected confirmed evidence")
			}
		})
	}
}
