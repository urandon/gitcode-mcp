package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/localgit"
)

func TestLocalProjectBackupConfigurationRefusalsAreCredentialFree(t *testing.T) {
	src := localSetupSource{root: t.TempDir()}
	src.path = filepath.Join(src.root, "trusted.yaml")
	req := LocalProjectSetupRequest{Project: "lab", ProjectID: "lab", StorageRef: "data", Ref: "refs/heads/main", Confirm: true, Initialize: true}
	if _, err := SetupLocalProject(context.Background(), src, req); err != nil {
		t.Fatal("fixture setup")
	}
	bundle := filepath.Join(t.TempDir(), "private.bundle")
	backup := LocalProjectBackupRequest{LocalProjectSetupRequest: req, Bundle: bundle}
	r, err := ExportLocalProject(context.Background(), src, backup)
	if err != nil || r.Status != "exported" {
		t.Fatal("fixture backup")
	}
	before, _ := os.ReadFile(src.path)
	backup.ExpectedHead = r.Commit
	if _, err = RestoreLocalProject(context.Background(), src, backup); err == nil {
		t.Fatal("registered target accepted")
	}
	after, _ := os.ReadFile(src.path)
	if !bytes.Equal(before, after) {
		t.Fatal("restore refusal changed registration")
	}
	for _, data := range [][]byte{[]byte("not: [valid"), bytes.Repeat([]byte("x"), (1<<20)+1)} {
		if os.WriteFile(src.path, data, 0600) != nil {
			t.Fatal("invalid fixture")
		}
		if _, err = ExportLocalProject(context.Background(), src, backup); err == nil {
			t.Fatal("invalid trusted config accepted")
		}
		if _, err = RestoreLocalProject(context.Background(), src, backup); err == nil {
			t.Fatal("invalid restore config accepted")
		}
	}
}

type restoreRaceSource struct {
	localSetupSource
	armed, changed bool
}

func (s *restoreRaceSource) UserConfigDir() (string, error) {
	if s.armed && !s.changed {
		s.changed = true
		data := []byte("service: {mode: local}\nlocal_projects:\n  lab: {backend: local_git, project_id: other, storage_ref: other, ref: refs/heads/main, read_only: false}\n")
		if err := os.WriteFile(s.path, data, 0600); err != nil {
			return "", err
		}
	}
	return s.localSetupSource.UserConfigDir()
}

func TestRestoreRegistrationRacePreservesVerifiedGitAndRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := localgit.Initialize(ctx, filepath.Join(t.TempDir(), "source.git"), "lab", "refs/heads/main")
	if err != nil {
		t.Fatal("source fixture")
	}
	bundle := filepath.Join(t.TempDir(), "private.bundle")
	exported, err := store.ExportBundle(ctx, "refs/heads/main", "lab", bundle)
	if err != nil {
		t.Fatal("fixture bundle")
	}
	src := &restoreRaceSource{localSetupSource: localSetupSource{root: t.TempDir()}}
	src.path = filepath.Join(src.root, "trusted.yaml")
	if os.WriteFile(src.path, []byte("format: json\n"), 0600) != nil {
		t.Fatal("config fixture")
	}
	src.armed = true
	req := LocalProjectBackupRequest{LocalProjectSetupRequest: LocalProjectSetupRequest{Project: "lab", ProjectID: "lab", StorageRef: "restored", Ref: "refs/heads/main", Confirm: true}, Bundle: bundle, ExpectedHead: exported.Commit}
	if _, err = RestoreLocalProject(ctx, src, req); err == nil || !src.changed {
		t.Fatal("registration race accepted")
	}
	path := filepath.Join(src.root, "config", "gitcode-mcp", "local-projects", "restored.git")
	restored, err := localgit.Open(ctx, path, localgit.Limits{})
	if err != nil {
		t.Fatal("verified store deleted on config refusal")
	}
	snap, err := restored.Snapshot(ctx, "refs/heads/main")
	if err != nil || snap.Project().ID != "lab" || snap.Commit() != exported.Commit {
		t.Fatal("verified Git changed")
	}
	if os.WriteFile(src.path, []byte("format: json\n"), 0600) != nil {
		t.Fatal("explicit config repair")
	}
	if _, err = SetupLocalProject(ctx, src, req.LocalProjectSetupRequest); err != nil {
		t.Fatal("explicit register recovery")
	}
}
