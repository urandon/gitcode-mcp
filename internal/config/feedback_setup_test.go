package config

import (
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
	if err != nil || replay.Status != "already_configured" {
		t.Fatalf("replay=%#v err=%v", replay, err)
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
