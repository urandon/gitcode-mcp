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
)

func legacyService(t *testing.T, goos string) (Manager, Paths) {
	t.Helper()
	m := newTestManager(t, goos)
	paths, err := m.ResolvePaths()
	if err != nil {
		t.Fatal("resolve fixture service")
	}
	if err := ensurePathDirs(paths); err != nil {
		t.Fatal("create fixture service")
	}
	if err := writePrivateFile(paths.InstallPath, []byte(legacyInstallFileContent(paths.InstallKind, m.BinaryPath, paths))); err != nil {
		t.Fatal("write legacy definition")
	}
	if err := os.WriteFile(filepath.Join(paths.LogDir, "service.out.log"), []byte("synthetic-private-output"), 0600); err != nil {
		t.Fatal("write legacy output")
	}
	return m, paths
}
func TestObservationUpgradePlanIsReadOnlyAndPublicSafe(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			m, paths := legacyService(t, goos)
			calls := 0
			m.Runner = func(context.Context, string, ...string) error { calls++; return nil }
			m.OutputRunner = func(context.Context, string, ...string) (string, error) { calls++; return "", nil }
			p, err := m.PlanObservationUpgrade()
			if err != nil || p.State != "confirmation_required" {
				t.Fatal("cannot render fixture plan")
			}
			if calls != 0 {
				t.Fatal("plan contacted platform manager")
			}
			b, _ := json.Marshal(p)
			for _, sentinel := range []string{paths.LogDir, paths.InstallPath, m.BinaryPath, "synthetic-private-output"} {
				if strings.Contains(string(b), sentinel) {
					t.Fatal("private coordinates or bytes leaked")
				}
			}
			if _, err := m.Install(true); err == nil {
				t.Fatal("install silently changed legacy output")
			}
			if _, err := m.Repair(context.Background()); err == nil {
				t.Fatal("repair silently changed legacy output")
			}
			if calls != 0 {
				t.Fatal("unconfirmed migration stopped service")
			}
			if _, err := m.ApplyObservationUpgrade(context.Background(), p.PlanID+"-stale"); err == nil {
				t.Fatal("stale plan accepted")
			}
			if calls != 0 {
				t.Fatal("stale plan mutated platform")
			}
		})
	}
}
func TestObservationUpgradeRefusesActiveOwner(t *testing.T) {
	m, paths := legacyService(t, "darwin")
	m.StartupTimeout = 15 * time.Millisecond
	m.StartupInterval = time.Millisecond
	if err := writeState(paths, State{PID: os.Getpid()}); err != nil {
		t.Fatal("write active owner fixture")
	}
	m.OutputRunner = func(context.Context, string, ...string) (string, error) { return "state = running", nil }
	m.Runner = func(context.Context, string, ...string) error { return nil }
	p, err := m.PlanObservationUpgrade()
	if err != nil {
		t.Fatal("render active plan")
	}
	_, err = m.ApplyObservationUpgrade(context.Background(), p.PlanID)
	var coded RPCDomainError
	if !errors.As(err, &coded) || coded.Code != "observation_owner_still_active" {
		t.Fatal("active owner not refused")
	}
	if _, err := os.Stat(filepath.Join(paths.LogDir, "service.out.log")); err != nil {
		t.Fatal("active legacy output removed")
	}
	if boundedDefinition(paths.InstallKind, paths.InstallPath) {
		t.Fatal("active definition replaced")
	}
}
func TestObservationUpgradeQuiescesThenCleansAndRestarts(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			m, paths := legacyService(t, goos)
			stopped := false
			m.StartupTimeout = time.Second
			m.StartupInterval = time.Millisecond
			m.OutputRunner = func(context.Context, string, ...string) (string, error) { return "state = running", nil }
			m.Runner = func(_ context.Context, name string, args ...string) error {
				command := strings.Join(args, " ")
				if strings.Contains(command, "bootout") || strings.Contains(command, " stop ") {
					stopped = true
					return nil
				}
				if !stopped {
					t.Error("replacement started before quiescence")
				}
				if strings.Contains(command, "kickstart") || strings.Contains(command, " start ") {
					if !boundedDefinition(paths.InstallKind, paths.InstallPath) {
						t.Error("start did not install bounded definition")
					}
					if _, err := os.Stat(filepath.Join(paths.LogDir, "service.out.log")); !os.IsNotExist(err) {
						t.Error("legacy file survived cleanup")
					}
					if err := writeState(paths, State{PID: os.Getpid()}); err != nil {
						return err
					}
					startTestUnixListener(t, paths.SocketPath)
				}
				return nil
			}
			p, err := m.PlanObservationUpgrade()
			if err != nil {
				t.Fatal("render upgrade plan")
			}
			result, err := m.ApplyObservationUpgrade(context.Background(), p.PlanID)
			if err != nil || result.State != "applied" || !stopped {
				t.Fatal("upgrade did not finish in order")
			}
		})
	}
}
func TestCacheMigrationPreservesLegacyRouting(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			m, paths := legacyService(t, goos)
			if _, err := m.InstallForCacheMigration(); err != nil {
				t.Fatal("compatible reinstall failed")
			}
			if boundedDefinition(paths.InstallKind, paths.InstallPath) {
				t.Fatal("cache migration silently upgraded output")
			}
			if _, err := os.Stat(filepath.Join(paths.LogDir, "service.out.log")); err != nil {
				t.Fatal("cache migration deleted legacy evidence")
			}
		})
	}
}
func TestObservationUpgradeRejectsUnsafeLegacyFile(t *testing.T) {
	m, paths := legacyService(t, "darwin")
	name := filepath.Join(paths.LogDir, "service.out.log")
	if err := os.Remove(name); err != nil {
		t.Fatal("replace fixture leaf")
	}
	target := filepath.Join(paths.RuntimeDir, "external-fixture")
	if err := os.WriteFile(target, []byte("synthetic-target-marker"), 0600); err != nil {
		t.Fatal("write target")
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal("link target")
	}
	if _, err := m.PlanObservationUpgrade(); err == nil {
		t.Fatal("symlink legacy plan accepted")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "synthetic-target-marker" {
		t.Fatal("external fixture modified")
	}
}
func TestJobShutdownWaitsForWorkersAndRejectsNewAdmission(t *testing.T) {
	m := NewJobManager("")
	job, err := m.StartFake(context.Background(), StartFakeJobRequest{Steps: 100, IntervalMS: 10})
	if err != nil {
		t.Fatal("start fake")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !m.shutdown(ctx) {
		t.Fatal("worker did not unwind")
	}
	m.mu.Lock()
	active := len(m.inflightWorkers)
	m.mu.Unlock()
	if active != 0 {
		t.Fatal("worker exited late")
	}
	if _, err := m.StartFake(context.Background(), StartFakeJobRequest{}); err == nil {
		t.Fatal("new worker admitted during shutdown")
	}
	got, _ := m.Get(job.ID)
	if got.Status != JobStatusCancelled {
		t.Fatal("worker cancellation not recorded")
	}
}
func TestJobShutdownTimeoutDoesNotClaimClean(t *testing.T) {
	m := NewJobManager("")
	m.markWorkerStarted("fixture-stalled-worker")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if m.shutdown(ctx) {
		t.Fatal("stalled worker incorrectly clean")
	}
	m.markWorkerFinished("fixture-stalled-worker")
}
func TestObservationUpgradePinsTargetExecutableBytes(t *testing.T) {
	m, _ := legacyService(t, "darwin")
	p, err := m.PlanObservationUpgrade()
	if err != nil {
		t.Fatal("render fixture plan")
	}
	if err := os.WriteFile(m.BinaryPath, []byte("#!/bin/sh\n# changed fixture bytes\nexit 0\n"), 0700); err != nil {
		t.Fatal("change executable fixture")
	}
	calls := 0
	m.Runner = func(context.Context, string, ...string) error { calls++; return nil }
	m.OutputRunner = func(context.Context, string, ...string) (string, error) { calls++; return "", nil }
	if _, err := m.ApplyObservationUpgrade(context.Background(), p.PlanID); err == nil {
		t.Fatal("changed executable accepted")
	}
	if calls != 0 {
		t.Fatal("stale executable plan stopped owner")
	}
}

func TestUnknownPlatformStateDoesNotDeleteLegacy(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			m, paths := legacyService(t, goos)
			m.OutputRunner = func(context.Context, string, ...string) (string, error) {
				return "", errors.New("platform unavailable")
			}
			m.Runner = func(context.Context, string, ...string) error { return errors.New("platform unavailable") }
			p, err := m.PlanObservationUpgrade()
			if err != nil {
				t.Fatal("fixture plan")
			}
			_, err = m.ApplyObservationUpgrade(context.Background(), p.PlanID)
			var coded RPCDomainError
			if !errors.As(err, &coded) || coded.Code != "observation_quiesce_failed" {
				t.Fatal("unknown platform state was not refused")
			}
			if _, err := os.Stat(filepath.Join(paths.LogDir, "service.out.log")); err != nil {
				t.Fatal("legacy output deleted without successful stop")
			}
			if boundedDefinition(paths.InstallKind, paths.InstallPath) {
				t.Fatal("legacy definition changed without successful stop")
			}
		})
	}
}
