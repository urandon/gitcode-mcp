package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/config"
)

func policyManager(t *testing.T, goos, rule string) Manager {
	t.Helper()
	m := newTestManager(t, goos)
	dir, _ := m.Source.UserConfigDir()
	path := filepath.Join(dir, "gitcode-mcp", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("capabilities: {schema: 1, ceiling: "+rule+"}"), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	m.Source, _, err = config.PrepareCapabilityPolicy(m.Source, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type policyWorkingDirSource struct {
	config.Source
	cwd string
}

func (s policyWorkingDirSource) WorkingDir() (string, error) { return s.cwd, nil }

func requirePolicyError(t *testing.T, err error, code string) {
	t.Helper()
	var typed interface{ DiagnosticCode() string }
	if !errors.As(err, &typed) || typed.DiagnosticCode() != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestCapabilityPolicyIPCDenialBeforeDecodeAndOwnerEffects(t *testing.T) {
	m := policyManager(t, "linux", "{allow: []}")
	s := RPCServer{Manager: m}
	for _, method := range []string{"Jobs.StartSync", "Jobs.StartRAGIndex", "Maintenance.Enroll", "RepositoryDocs.RegisterSource", "LocalProjects.Mutate", "Observability.Summary"} {
		_, err := s.dispatch(context.Background(), method, json.RawMessage(`"invalid"`))
		requirePolicyError(t, err, "capability_policy_denied")
	}
	_, err := s.dispatch(context.Background(), "Jobs.FutureOperation", nil)
	requirePolicyError(t, err, "capability_policy_unmapped")
	if _, err := s.dispatch(context.Background(), "Policy.Status", nil); err != nil {
		t.Fatal(err)
	}
	jobs := NewJobManager("")
	if _, err := jobs.StartSync(context.Background(), m, StartSyncJobRequest{}); err == nil {
		t.Fatal("sync admission bypass")
	}
	if _, err := jobs.StartRAGIndex(context.Background(), m, StartRAGIndexJobRequest{}); err == nil {
		t.Fatal("RAG admission bypass")
	}
	if _, err := jobs.StartRepositoryDocsIndex(context.Background(), m, StartRepositoryDocsIndexJobRequest{}); err == nil {
		t.Fatal("docs admission bypass")
	}
	if len(jobs.List()) != 0 {
		t.Fatal("denied jobs were created")
	}
	maintenance := NewMaintenanceManager(m, jobs, "")
	if _, err := maintenance.Enroll(context.Background(), MaintenanceEnrollRequest{}); err == nil {
		t.Fatal("enrollment bypass")
	}
	if _, _, err := maintenance.RegisterRepositoryDocsSource(context.Background(), "", "", "", ""); err == nil {
		t.Fatal("Git inspection bypass")
	}
	if _, err := m.Repair(context.Background()); err == nil {
		t.Fatal("native repair bypass")
	}
	if err := m.Run(context.Background()); err == nil {
		t.Fatal("host launch bypass")
	}
}

func TestCapabilityPolicyPeerMismatchDoesNotDispatchMutation(t *testing.T) {
	reader := policyManager(t, "linux", "{allow: [sync_live, repo_status]}")
	other := policyManager(t, "linux", "{allow: []}")
	address := "policy-peer-" + reader.instancePolicy().Fingerprint()
	memoryRPC.Lock()
	memoryRPC.servers[address] = RPCServer{Manager: other}
	memoryRPC.Unlock()
	t.Cleanup(func() { memoryRPC.Lock(); delete(memoryRPC.servers, address); memoryRPC.Unlock() })
	client := &RPCClient{Network: "mem", Address: address, Policy: reader.instancePolicy()}
	err := client.Call(context.Background(), "Jobs.StartSync", nil, nil)
	requirePolicyError(t, err, "capability_policy_mismatch")
	client.Address = "missing-policy-peer"
	requirePolicyError(t, client.Call(context.Background(), "Jobs.StartSync", nil, nil), "capability_policy_unavailable")
	// A legacy peer is not assumed equivalent to a managed deployment.
	memoryRPC.Lock()
	memoryRPC.servers[address] = RPCServer{}
	memoryRPC.Unlock()
	client.Address = address
	requirePolicyError(t, client.Call(context.Background(), "Jobs.StartSync", nil, nil), "capability_policy_mismatch")
}

func TestCapabilityPolicyNativeDefinitionAndLostPolicy(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			m := newTestManager(t, goos)
			dir, _ := m.Source.UserConfigDir()
			rawSource := policyWorkingDirSource{Source: m.Source, cwd: dir}
			machine := filepath.Join(dir, "gitcode-mcp", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(machine), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(machine, []byte("capabilities: {schema: 1, ceiling: {allow: [service_lifecycle, service_host]}}"), 0600); err != nil {
				t.Fatal(err)
			}
			deployment := filepath.Join(dir, "instance with spaces & \"quotes\" % $.yaml")
			if err := os.WriteFile(deployment, []byte("capabilities: {schema: 1}"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			m.Source, _, err = config.PrepareCapabilityPolicy(rawSource, filepath.Base(deployment), "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.Install(false); err != nil {
				t.Fatal(err)
			}
			paths, err := m.ResolvePaths()
			if err != nil {
				t.Fatal(err)
			}
			if err := m.validateInstalledPolicy(paths); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(paths.InstallPath)
			if err != nil {
				t.Fatal(err)
			}
			args, err := policyDefinitionArguments(paths.InstallKind, string(data))
			if err != nil {
				t.Fatal(err)
			}
			_, gotPath, fp, err := config.ExtractPolicyLaunchFlags(args[1:])
			if err != nil || gotPath != deployment || fp != m.instancePolicy().Fingerprint() {
				t.Fatal("native policy identity lost")
			}
			rawSource.cwd = filepath.Join(dir, "different-native-cwd")
			if _, _, err := config.PrepareCapabilityPolicy(rawSource, gotPath, fp); err != nil {
				t.Fatal("native launch lost absolute deployment authority")
			}
			if err := os.Remove(deployment); err != nil {
				t.Fatal(err)
			}
			if _, _, err := config.PrepareCapabilityPolicy(rawSource, deployment, fp); err == nil {
				t.Fatal("missing deployment accepted")
			}
			if err := os.Remove(machine); err != nil {
				t.Fatal(err)
			}
			m.Source = rawSource
			requirePolicyError(t, m.validateInstalledPolicy(paths), "capability_policy_mismatch")
		})
	}
}

func TestCapabilityPolicyOwnerSnapshotOverridesRetainedGrants(t *testing.T) {
	m := policyManager(t, "linux", "{allow: []}")
	permissive, _ := capability.CompilePolicy(false, capability.PolicyLayer{Source: "machine", Specification: &capability.Specification{}})
	m.EffectiveConfig = &config.Config{CapabilityPolicy: permissive}
	eff, err := effectiveJobConfig(m, "synthetic-cache")
	if err != nil || eff.Config.CapabilityPolicy.Fingerprint() != m.instancePolicy().Fingerprint() {
		t.Fatal("retained config restored grants")
	}
	if strings.Contains(eff.Config.CapabilityPolicy.View().Fingerprint, "synthetic-cache") {
		t.Fatal("path in fingerprint")
	}
}

func TestCapabilityPolicyRecoveryPreservesStagesAndPendingReceipts(t *testing.T) {
	m := policyManager(t, "linux", "{allow: [service_host]}")
	m.RuntimeDir = t.TempDir()
	stagePath := filepath.Join(m.RuntimeDir, "stage-sentinel")
	if err := os.WriteFile(stagePath, []byte("durable-fetched-work"), 0600); err != nil {
		t.Fatal(err)
	}
	jobs := NewJobManager("")
	jobs.jobs["job-interrupted"] = &Job{ID: "job-interrupted", Type: SyncJobType, Status: JobStatusInterrupted, WorkKey: "retained-work"}
	if err := jobs.RecoverSyncStages(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	job, ok := jobs.Get("job-interrupted")
	if !ok || job.Status != JobStatusInterrupted || job.ErrorClass != "capability_policy_denied" || job.WorkKey != "retained-work" {
		t.Fatal("recovery lost policy evidence or work")
	}
	if b, err := os.ReadFile(stagePath); err != nil || string(b) != "durable-fetched-work" {
		t.Fatal("stage journal changed")
	}
	maintenance := NewMaintenanceManager(m, jobs, "")
	actions := NewJobActionManager("", jobs, maintenance)
	actions.receipts["receipt"] = jobActionReceiptDisk{KeyHash: "receipt", Receipt: adminhttp.JobActionReceipt{Action: "retry", Outcome: "pending"}}
	actions.reconcile = func(context.Context, string, string, string) (MaintenanceReconcileResult, error) {
		t.Fatal("policy-blocked pending retry redriven")
		return MaintenanceReconcileResult{}, nil
	}
	if actions.recoverPendingLocked(context.Background()) || actions.receipts["receipt"].Receipt.Outcome != "pending" {
		t.Fatal("pending receipt lost")
	}
	maintenance.entries["registration"] = &MaintenanceEntry{RegistrationID: "registration", Enabled: true, Policy: MaintenancePolicy{SyncEnabled: true}, cachePath: "unavailable-cache"}
	entry, _ := maintenance.reconcileEntry(context.Background(), "registration")
	if entry.State != "degraded" || entry.LastErrorClass != "capability_policy_denied" {
		t.Fatalf("policy-blocked registration: %+v", entry)
	}
}

func TestCapabilityPolicyHoldPersistenceFailureKeepsInterruptedWork(t *testing.T) {
	m := policyManager(t, "linux", "{allow: [service_host]}")
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	now := time.Now().UTC()
	jobs.jobs["job-held"] = &Job{ID: "job-held", Type: SyncJobType, Status: JobStatusInterrupted, CreatedAt: now, UpdatedAt: now, WorkKey: "retained-work"}
	jobs.writeFile = func(string, []byte, os.FileMode) error { return errors.New("private persistence diagnostic") }
	err := jobs.RecoverSyncStages(context.Background(), m)
	requirePolicyError(t, err, "capability_policy_hold_persistence_failed")
	if strings.Contains(err.Error(), "private persistence diagnostic") {
		t.Fatal("raw persistence error exposed")
	}
	released := false
	done := m.startSyncStageRecovery(context.Background(), jobs, func() { released = true })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("policy hold did not finish")
	}
	job, _ := jobs.Get("job-held")
	if released || job.Status != JobStatusInterrupted || job.WorkKey != "retained-work" {
		t.Fatal("failed hold discarded recovery or released its fence")
	}
}
