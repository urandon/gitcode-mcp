package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFeedbackSetupPlanApplyPreservesUnrelatedYAMLAndReplays(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	before := "# operator comment\nformat: json\nrag:\n  search:\n    top_k: 11\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMCPConfigPath, path)
	src := OSSource{}
	plan, err := PlanFeedbackSetup(src, "example/feedback")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "confirmation_required" || !plan.ConfirmationRequired || plan.PlanID == "" {
		t.Fatalf("plan=%#v", plan)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != before {
		t.Fatalf("planning changed config: %q err=%v", unchanged, err)
	}
	result, err := ApplyFeedbackSetup(plan, "setup-example-feedback", time.Date(2026, 9, 5, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "configured" || !strings.Contains(result.Evidence, "no credential") {
		t.Fatalf("result=%#v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# operator comment", "format: json", "top_k: 11", "enabled: true", "sink: gitcode_issues", "repo_id: example/feedback", "labels: feedback|dogfood"} {
		if !strings.Contains(string(after), want) {
			t.Fatalf("updated config missing %q:\n%s", want, after)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
	replayPlan, err := PlanFeedbackSetup(src, "example/feedback")
	if err != nil {
		t.Fatal(err)
	}
	if replayPlan.Status != "already_configured" || replayPlan.ConfirmationRequired {
		t.Fatalf("replay plan=%#v", replayPlan)
	}
	replay, err := ApplyFeedbackSetup(replayPlan, "setup-example-feedback", time.Now())
	if err != nil || replay.Status != "configured" || !replay.Replayed || replay.PlanID != result.PlanID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	journal, err := os.ReadFile(path + ".feedback-setup-receipts.json")
	if err != nil || bytes.Contains(journal, []byte("setup-example-feedback")) {
		t.Fatalf("journal missing or exposed raw idempotency key: %s err=%v", journal, err)
	}
}

func TestFeedbackSetupRejectsStalePlanAndInvalidTarget(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("format: text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMCPConfigPath, path)
	if _, err := PlanFeedbackSetup(OSSource{}, "not-a-repository"); err == nil {
		t.Fatal("invalid repository id accepted")
	}
	if _, err := PlanFeedbackSetup(OSSource{}, "example/repo;command"); err == nil {
		t.Fatal("shell-unsafe repository id accepted")
	}
	plan, err := PlanFeedbackSetup(OSSource{}, "example/feedback")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("format: markdown\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFeedbackSetup(plan, "stale-plan", time.Now()); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("err=%v", err)
	}
}

func TestFeedbackSetupPreservesExplicitDuplicatePolicyAndLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "feedback:\n  enabled: false\n  sink: gitcode_issues\n  repo_id: old/repo\n  labels: ux|agent\n  duplicate_policy: return_existing\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMCPConfigPath, path)
	plan, err := PlanFeedbackSetup(OSSource{}, "example/feedback")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.Labels, ",") != "ux,agent" || plan.DuplicatePolicy != "return_existing" {
		t.Fatalf("plan=%#v", plan)
	}
}

func TestFeedbackSetupRejectsNonMappingYAMLWithoutMutation(t *testing.T) {
	for _, content := range []string{"- list\n- root\n", "feedback: enabled\n"} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(content), "\n", "_"), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(EnvMCPConfigPath, path)
			if _, err := PlanFeedbackSetup(OSSource{}, "example/feedback"); err == nil || !strings.Contains(err.Error(), "must be a mapping") {
				t.Fatalf("err=%v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != content {
				t.Fatalf("config mutated: %q err=%v", after, err)
			}
		})
	}
}

func TestFeedbackSetupDoesNotTreatUnreadableConfigAsMissing(t *testing.T) {
	src := newMemorySource(t)
	path := filepath.Join(src.configDir, "config.yaml")
	src.env[EnvMCPConfigPath] = path
	src.files[path] = []byte("format: json\n")
	src.readErr[path] = os.ErrPermission
	if _, err := PlanFeedbackSetup(src, "example/feedback"); err == nil || !strings.Contains(err.Error(), "cannot be read") || strings.Contains(err.Error(), path) {
		t.Fatalf("err=%v", err)
	}
}

func TestFeedbackSetupLockDoesNotLeaveProcessCrashSentinel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	release, err := acquireFeedbackSetupLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireFeedbackSetupLock(path); err == nil || !strings.Contains(err.Error(), "another configuration update") {
		t.Fatalf("concurrent err=%v", err)
	}
	release()
	releaseAgain, err := acquireFeedbackSetupLock(path)
	if err != nil {
		t.Fatalf("persistent lock file blocked a new owner: %v", err)
	}
	releaseAgain()
}

func TestFeedbackSetupRejectsSameKeyForDifferentIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("format: text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMCPConfigPath, path)
	first, err := PlanFeedbackSetup(OSSource{}, "example/first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFeedbackSetup(first, "stable-setup-key", time.Now()); err != nil {
		t.Fatal(err)
	}
	second, err := PlanFeedbackSetup(OSSource{}, "example/second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFeedbackSetup(second, "stable-setup-key", time.Now()); err == nil || !strings.Contains(err.Error(), "different setup intent") {
		t.Fatalf("err=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(after), "repo_id: example/first") || strings.Contains(string(after), "repo_id: example/second") {
		t.Fatalf("config retargeted: %s err=%v", after, err)
	}
}

func TestFeedbackSetupRecoversPendingReceiptFromConfigDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("format: text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMCPConfigPath, path)
	plan, err := PlanFeedbackSetup(OSSource{}, "example/feedback")
	if err != nil {
		t.Fatal(err)
	}
	key := "recover-setup-key"
	generated := time.Date(2026, 9, 5, 6, 0, 0, 0, time.UTC)
	journal := feedbackSetupJournal{Schema: feedbackSetupSchema, Claims: map[string]feedbackSetupClaim{
		digestBytes([]byte(key)): {PlanID: plan.PlanID, IntentDigest: plan.intentDigest, BeforeDigest: plan.beforeDigest, AfterDigest: plan.afterDigest, Status: "pending", ResultStatus: "configured", GeneratedAt: generated},
	}}
	if err := writeFeedbackSetupJournal(path+".feedback-setup-receipts.json", journal); err != nil {
		t.Fatal(err)
	}
	if err := durableWriteFeedbackConfig(path, plan.next); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyFeedbackSetup(plan, key, time.Now())
	if err != nil || !result.Replayed || result.Status != "configured" || !result.GeneratedAt.Equal(generated) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestFeedbackSetupCompactsOldestTerminalReceiptAtCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "feedback:\n  enabled: true\n  sink: gitcode_issues\n  repo_id: example/feedback\n  labels: feedback|dogfood\n  duplicate_policy: suggest\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvMCPConfigPath, path)
	plan, err := PlanFeedbackSetup(OSSource{}, "example/feedback")
	if err != nil || plan.Status != "already_configured" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	oldestKey := digestBytes([]byte("capacity-key-000"))
	retainedKey := digestBytes([]byte("capacity-key-255"))
	journal := feedbackSetupJournal{Schema: feedbackSetupSchema, Claims: make(map[string]feedbackSetupClaim, feedbackSetupJournalLimit)}
	for i := 0; i < feedbackSetupJournalLimit; i++ {
		key := digestBytes([]byte(fmt.Sprintf("capacity-key-%03d", i)))
		journal.Claims[key] = feedbackSetupClaim{
			PlanID: plan.PlanID, IntentDigest: plan.intentDigest, BeforeDigest: plan.beforeDigest, AfterDigest: plan.afterDigest,
			Status: "succeeded", ResultStatus: "already_configured", GeneratedAt: now.Add(time.Duration(i-feedbackSetupJournalLimit) * time.Minute),
		}
	}
	journalPath := path + ".feedback-setup-receipts.json"
	if err := writeFeedbackSetupJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyFeedbackSetup(plan, "capacity-new-key", now)
	if err != nil || result.Status != "already_configured" || result.Replayed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	journal, err = readFeedbackSetupJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Claims) != feedbackSetupJournalLimit {
		t.Fatalf("claims=%d", len(journal.Claims))
	}
	if _, exists := journal.Claims[oldestKey]; exists {
		t.Fatal("oldest terminal receipt was not compacted")
	}
	if _, exists := journal.Claims[retainedKey]; !exists {
		t.Fatal("recent retained receipt was compacted")
	}
	replay, err := ApplyFeedbackSetup(plan, "capacity-key-255", now.Add(time.Minute))
	if err != nil || !replay.Replayed || replay.PlanID != plan.PlanID {
		t.Fatalf("retained replay=%#v err=%v", replay, err)
	}
	different, err := PlanFeedbackSetup(OSSource{}, "example/different")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFeedbackSetup(different, "capacity-key-255", now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "different setup intent") {
		t.Fatalf("retained conflict err=%v", err)
	}
}

func TestFeedbackSetupReceiptRetentionNeverPrunesPendingClaims(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	old := now.Add(-feedbackSetupReceiptTTL - time.Second)
	journal := feedbackSetupJournal{Schema: feedbackSetupSchema, Claims: map[string]feedbackSetupClaim{
		"pending":   {Status: "pending", GeneratedAt: old},
		"succeeded": {Status: "succeeded", GeneratedAt: old},
		"abandoned": {Status: "abandoned", GeneratedAt: old},
		"recent":    {Status: "succeeded", GeneratedAt: now.Add(-time.Hour)},
	}}
	if !pruneExpiredFeedbackSetupClaims(&journal, now) {
		t.Fatal("expected expired terminal receipts to be pruned")
	}
	if _, exists := journal.Claims["pending"]; !exists {
		t.Fatal("pending claim was pruned")
	}
	if _, exists := journal.Claims["recent"]; !exists {
		t.Fatal("recent terminal claim was pruned")
	}
	if _, exists := journal.Claims["succeeded"]; exists {
		t.Fatal("expired succeeded claim was retained")
	}
	if _, exists := journal.Claims["abandoned"]; exists {
		t.Fatal("expired abandoned claim was retained")
	}
}
