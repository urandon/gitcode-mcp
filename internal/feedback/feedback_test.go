package feedback

import (
	"gitcode-mcp/internal/buildinfo"
	"strings"
	"testing"
	"time"
)

func validDraft() Draft {
	return Draft{
		Summary:           "Bulk issue sync returns malformed JSON",
		Category:          "bug",
		Surface:           "sync",
		ReporterType:      "agent",
		Goal:              "Refresh the cached issue collection before autonomous triage",
		Circumstances:     "During an MCP live sync against a bound repository after the cached head became stale",
		Observed:          "sync_live failed with partial_response",
		Expected:          "The issue collection sync completes",
		Impact:            "The agent had to fall back to an exact issue sync",
		ToolName:          "sync_live",
		FailureClass:      "partial_response",
		ReproductionSteps: []string{"Call sync_live for the issue collection", "Observe partial_response before a usable result"},
		FallbackUsed:      "An exact issue sync was used instead",
		AcceptanceSignal:  "The bounded collection sync returns a complete result or typed partial result",
	}
}

func testContext() RuntimeContext {
	return RuntimeContext{Version: "v1.2.3", Commit: "abc123", ProviderMode: "live", CacheSchemaVersion: 7, ExpectedSchema: 7, SchemaCompatible: true, SinkBindingState: "configured", OSFamily: "darwin", ObservedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
}

func TestBuildOwnedDestinationAndLegacyMigration(t *testing.T) {
	for _, labels := range [][]string{{"feedback", "dogfood"}, {"dogfood", "feedback"}, {"dogfood", "feedback", "dogfood"}, {"feedback", "other"}} {
		cfg, err := NormalizeConfig(Config{Enabled: true, Labels: labels})
		conflict := contains(labels, "other")
		if err != nil || cfg.ConfigurationConflict != conflict || strings.Join(cfg.Labels, "|") != "feedback|dogfood" {
			t.Fatalf("labels=%v config=%+v err=%v", labels, cfg, err)
		}
	}
	for _, legacy := range []string{"", "urandon/gitcode-mcp", "example/other", "https://user:secret@example.invalid/?token=secret"} {
		cfg, err := NormalizeConfig(Config{Enabled: true, RepoID: legacy})
		if err != nil {
			t.Fatal(err)
		}
		conflict := legacy != "" && legacy != "urandon/gitcode-mcp"
		if cfg.RepoID != "urandon/gitcode-mcp" || cfg.ConfigurationConflict != conflict {
			t.Fatalf("config=%+v", cfg)
		}
		again, err := NormalizeConfig(cfg)
		if err != nil || again.ConfigurationConflict != conflict {
			t.Fatalf("conflict lost: %+v err=%v", again, err)
		}
		ready := EvaluateReadiness(ReadinessInput{Config: again, RepositoryBound: true, CredentialPresent: true, ProviderAvailable: true})
		if ready.SubmitAvailable == conflict || !ready.PrepareAvailable || strings.Contains(ready.Remediation, "secret") {
			t.Fatalf("readiness=%+v", ready)
		}
	}
	previous := buildinfo.FeedbackRepository
	t.Cleanup(func() { buildinfo.FeedbackRepository = previous })
	buildinfo.FeedbackRepository = "example/distribution"
	cfg, err := NormalizeConfig(Config{Enabled: true})
	if err != nil || cfg.RepoID != "example/distribution" || cfg.ConfigurationConflict {
		t.Fatalf("downstream=%+v err=%v", cfg, err)
	}
	cfg, err = NormalizeConfig(Config{Enabled: true, RepoID: "urandon/gitcode-mcp"})
	if err != nil || !cfg.ConfigurationConflict {
		t.Fatalf("downstream legacy conflict=%+v err=%v", cfg, err)
	}
	buildinfo.FeedbackRepository = "https://user:secret@example.invalid"
	ready := EvaluateReadiness(ReadinessInput{Config: Config{Enabled: true}, RepositoryBound: true, CredentialPresent: true, ProviderAvailable: true})
	if ready.State != ReadinessConfigurationConflict || ready.RepoID != "" || ready.SubmitAvailable {
		t.Fatalf("invalid build identity=%+v", ready)
	}
}

func TestPrepareRendersDeterministicPublicSafeReport(t *testing.T) {
	cfg := Config{Enabled: true, RepoID: "urandon/gitcode-mcp", Labels: []string{"feedback", "dogfood", "feedback"}}
	draft := validDraft()
	draft.Evidence = []string{
		"request https://user:pass@example.test/api?access_token=secret#fragment failed",
		"internal tracker https://tracker.corp.local/private-owner/private-repo",
		"public ticket https://gitcode.com/urandon/gitcode-mcp/issues/42?token=secret#note",
		"cache /Users/alice/private/cache.db was used",
		"Authorization: Bearer super-secret-token",
	}

	first, err := Prepare(draft, testContext(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(draft, testContext(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != second.Fingerprint || first.Status != "prepared" || first.DedupeDecision != "none" {
		t.Fatalf("unexpected prepared report: %#v", first)
	}
	for _, secret := range []string{"user:pass", "access_token", "super-secret-token", "/Users/alice", "#fragment", "tracker.corp.local", "private-owner"} {
		if strings.Contains(first.Body, secret) {
			t.Fatalf("body leaked %q: %s", secret, first.Body)
		}
	}
	if !strings.Contains(first.Body, "[REDACTED_URL]") || !strings.Contains(first.Body, "https://gitcode.com/urandon/gitcode-mcp/issues/42") {
		t.Fatalf("URL policy not reflected in body: %s", first.Body)
	}
	for _, section := range []string{"## Goal", "## Circumstances", "## Observed behavior", "## Expected behavior", "## Impact", FingerprintMarker(first.Fingerprint)} {
		if !strings.Contains(first.Body, section) {
			t.Fatalf("body missing %q", section)
		}
	}
	if first.RedactionsApplied == 0 {
		t.Fatal("expected redactions to be reported")
	}
}

func TestPrepareReturnsTargetedQuestionsForIncompleteContext(t *testing.T) {
	draft := validDraft()
	draft.Goal = ""
	draft.Circumstances = "does not work"
	draft.ReproductionSteps = nil
	draft.FallbackUsed = "unknown"
	draft.AcceptanceSignal = "TBD"
	prepared, err := Prepare(draft, testContext(), Config{Enabled: true, RepoID: "urandon/gitcode-mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "needs_context" || len(prepared.MissingFields) != 5 || len(prepared.FollowUpQuestions) != 5 {
		t.Fatalf("prepared=%#v", prepared)
	}
	for _, field := range []string{"goal", "circumstances", "reproduction_steps", "fallback_used", "acceptance_signal"} {
		if !contains(prepared.MissingFields, field) {
			t.Fatalf("missing fields %v do not contain %q", prepared.MissingFields, field)
		}
	}
}

func TestPrepareRejectsForbiddenRawContentInEveryNarrativeShape(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Draft)
	}{
		{name: "goal transcript", mutate: func(draft *Draft) { draft.Goal = "full transcript follows" }},
		{name: "circumstances payload", mutate: func(draft *Draft) { draft.Circumstances = "raw api response body follows" }},
		{name: "reproduction environment", mutate: func(draft *Draft) { draft.ReproductionSteps = []string{"capture an environment dump"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			draft := validDraft()
			tt.mutate(&draft)
			if prepared, err := Prepare(draft, testContext(), Config{Enabled: true, RepoID: "urandon/gitcode-mcp"}, nil); !IsValidationError(err) {
				t.Fatalf("prepared=%#v err=%v, want validation error", prepared, err)
			}
		})
	}
}

func TestPrepareAllowsExplicitNoFallbackButRejectsNoneElsewhere(t *testing.T) {
	draft := validDraft()
	draft.FallbackUsed = "none"
	prepared, err := Prepare(draft, testContext(), Config{Enabled: true, RepoID: "urandon/gitcode-mcp"}, nil)
	if err != nil || prepared.Status != "prepared" {
		t.Fatalf("explicit no fallback prepared=%#v err=%v", prepared, err)
	}

	draft.Goal = "none"
	draft.ReproductionSteps = []string{"none"}
	prepared, err = Prepare(draft, testContext(), Config{Enabled: true, RepoID: "urandon/gitcode-mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"goal", "reproduction_steps"} {
		if !contains(prepared.MissingFields, field) {
			t.Fatalf("missing fields %v do not contain %q", prepared.MissingFields, field)
		}
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestPrepareDedupeContract(t *testing.T) {
	cfg := Config{Enabled: true, RepoID: "urandon/gitcode-mcp"}
	draft := validDraft()
	base, err := Prepare(draft, testContext(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	existing := []ExistingIssue{{ID: "ISSUE-42", Number: 42, Status: "open", Title: base.Title, Body: FingerprintMarker(base.Fingerprint), URL: "https://gitcode.com/urandon/gitcode-mcp/issues/42"}}
	exact, err := Prepare(draft, testContext(), cfg, existing)
	if err != nil {
		t.Fatal(err)
	}
	if exact.Status != "duplicate" || exact.DedupeDecision != "exact_match" || len(exact.Candidates) != 1 {
		t.Fatalf("exact duplicate result: %#v", exact)
	}

	other := validDraft()
	other.Summary = "Bulk issue sync returns truncated malformed JSON"
	likelyBase, err := Prepare(other, testContext(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	likelyExisting := []ExistingIssue{{ID: "ISSUE-43", Number: 43, Status: "open", Title: "[Feedback/bug][sync] Bulk issue sync returns malformed JSON"}}
	if likelyBase.Fingerprint == base.Fingerprint {
		t.Fatal("distinct report unexpectedly shared fingerprint")
	}
	likely, err := Prepare(other, testContext(), cfg, likelyExisting)
	if err != nil {
		t.Fatal(err)
	}
	if likely.Status != "duplicate_candidates" || likely.DedupeDecision != "likely_match" {
		t.Fatalf("likely duplicate result: %#v", likely)
	}
	returnConfig := cfg
	returnConfig.DuplicatePolicy = DuplicatePolicyReturn
	returned, err := Prepare(other, testContext(), returnConfig, likelyExisting)
	if err != nil {
		t.Fatal(err)
	}
	if returned.Status != "duplicate" || returned.DedupeDecision != "likely_match" {
		t.Fatalf("return_existing result: %#v", returned)
	}
	other.DuplicateOverride = DuplicateOverrideCreate
	override, err := Prepare(other, testContext(), cfg, likelyExisting)
	if err != nil {
		t.Fatal(err)
	}
	if override.Status != "prepared" || override.DedupeDecision != "likely_match" {
		t.Fatalf("override result: %#v", override)
	}

	generic := []ExistingIssue{{ID: "ISSUE-4226732", Status: "open", Title: "Issue 4226732"}}
	noFalsePositive, err := Prepare(draft, testContext(), cfg, generic)
	if err != nil {
		t.Fatal(err)
	}
	if noFalsePositive.DedupeDecision != "none" {
		t.Fatalf("generic placeholder became duplicate: %#v", noFalsePositive.Candidates)
	}
}

func TestPrepareRejectsUnsafeEvidenceAndExplainsMissingSetup(t *testing.T) {
	draft := validDraft()
	draft.Evidence = []string{"full transcript follows"}
	if _, err := Prepare(draft, testContext(), DefaultConfig(), nil); !IsValidationError(err) {
		t.Fatalf("err=%v, want validation error", err)
	}
	draft.Evidence = nil
	prepared, err := Prepare(draft, testContext(), DefaultConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Configured || prepared.Status != "configuration_required" || prepared.Remediation == "" {
		t.Fatalf("missing setup result: %#v", prepared)
	}
}

func TestEvaluateReadinessPrecedence(t *testing.T) {
	readyConfig := Config{Enabled: true, Sink: SinkGitCodeIssues, RepoID: "urandon/gitcode-mcp"}
	tests := []struct {
		name  string
		input ReadinessInput
		want  string
	}{
		{name: "disabled", input: ReadinessInput{Config: DefaultConfig()}, want: ReadinessDisabled},
		{name: "configuration conflict", input: ReadinessInput{Config: Config{Enabled: true, RepoID: "example/other"}, RepositoryBound: true, CredentialPresent: true, ProviderAvailable: true}, want: ReadinessConfigurationConflict},
		{name: "repository unbound", input: ReadinessInput{Config: readyConfig, CredentialPresent: true, ProviderAvailable: true}, want: ReadinessRepositoryUnbound},
		{name: "credential missing", input: ReadinessInput{Config: readyConfig, RepositoryBound: true, ProviderAvailable: true}, want: ReadinessCredentialMissing},
		{name: "provider unavailable", input: ReadinessInput{Config: readyConfig, RepositoryBound: true, CredentialPresent: true}, want: ReadinessProviderUnavailable},
		{name: "ready", input: ReadinessInput{Config: readyConfig, RepositoryBound: true, CredentialPresent: true, ProviderAvailable: true}, want: ReadinessReady},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateReadiness(tt.input)
			if got.State != tt.want || !got.PrepareAvailable || got.SubmitAvailable != (tt.want == ReadinessReady) || len(got.Checks) != 5 {
				t.Fatalf("readiness=%#v", got)
			}
			if tt.want != ReadinessReady && (got.Remediation == "" || got.Handoff == "") {
				t.Fatalf("blocked readiness lacks remediation: %#v", got)
			}
		})
	}
}

func TestExplicitEmptySinkRemainsVisibleToReadiness(t *testing.T) {
	cfg, err := NormalizeConfig(Config{Enabled: true, SinkExplicit: true, RepoID: "urandon/gitcode-mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ConfigurationConflict {
		t.Fatal("explicit empty legacy sink must be diagnosed")
	}
	result := EvaluateReadiness(ReadinessInput{Config: cfg, RepositoryBound: true, CredentialPresent: true, ProviderAvailable: true})
	if result.State != ReadinessConfigurationConflict {
		t.Fatalf("readiness=%#v", result)
	}
}

func TestFeedbackSetupHandoffRejectsShellMetacharacters(t *testing.T) {
	for _, repoID := range []string{"example/repo;command", "example/repo`command`", "example/repo$(command)", "example/.hidden", "example/repo"} {
		handoff := feedbackBindingHandoff(repoID)
		valid := repoID == "example/repo"
		if valid && handoff != "gitcode-mcp repo add --repo example/repo --owner example --name repo" {
			t.Fatalf("valid handoff=%q", handoff)
		}
		if !valid && handoff != "gitcode-mcp feedback status" {
			t.Fatalf("unsafe handoff for %q: %q", repoID, handoff)
		}
	}
}
