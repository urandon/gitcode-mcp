package feedback

import (
	"strings"
	"testing"
	"time"
)

func validDraft() Draft {
	return Draft{
		Summary:      "Bulk issue sync returns malformed JSON",
		Category:     "bug",
		Surface:      "sync",
		ReporterType: "agent",
		Observed:     "sync_live failed with partial_response",
		Expected:     "The issue collection sync completes",
		Impact:       "The agent had to fall back to an exact issue sync",
		ToolName:     "sync_live",
		FailureClass: "partial_response",
	}
}

func testContext() RuntimeContext {
	return RuntimeContext{Version: "v1.2.3", Commit: "abc123", ProviderMode: "live", CacheSchemaVersion: 7, ExpectedSchema: 7, SchemaCompatible: true, SinkBindingState: "configured", OSFamily: "darwin", ObservedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
}

func TestPrepareRendersDeterministicPublicSafeReport(t *testing.T) {
	cfg := Config{Enabled: true, RepoID: "example/tool", Labels: []string{"feedback", "feedback"}}
	draft := validDraft()
	draft.Evidence = []string{
		"request https://user:pass@example.test/api?access_token=secret#fragment failed",
		"internal tracker https://tracker.corp.local/private-owner/private-repo",
		"public ticket https://gitcode.com/example/tool/issues/42?token=secret#note",
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
	if !strings.Contains(first.Body, "[REDACTED_URL]") || !strings.Contains(first.Body, "https://gitcode.com/example/tool/issues/42") {
		t.Fatalf("URL policy not reflected in body: %s", first.Body)
	}
	for _, section := range []string{"## Observed behavior", "## Expected behavior", "## Impact", FingerprintMarker(first.Fingerprint)} {
		if !strings.Contains(first.Body, section) {
			t.Fatalf("body missing %q", section)
		}
	}
	if first.RedactionsApplied == 0 {
		t.Fatal("expected redactions to be reported")
	}
}

func TestPrepareDedupeContract(t *testing.T) {
	cfg := Config{Enabled: true, RepoID: "example/tool"}
	draft := validDraft()
	base, err := Prepare(draft, testContext(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	existing := []ExistingIssue{{ID: "ISSUE-42", Number: 42, Status: "open", Title: base.Title, Body: FingerprintMarker(base.Fingerprint), URL: "https://gitcode.com/example/tool/issues/42"}}
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
