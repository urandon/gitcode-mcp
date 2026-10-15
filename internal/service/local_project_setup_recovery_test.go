package service

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
)

func TestLocalProjectSetupInitializationPreservesConcurrentlyRegisteredData(t *testing.T) {
	src := localSetupSource{root: t.TempDir()}
	src.path = filepath.Join(src.root, "selected.yaml")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("fixture Git unavailable")
	}
	wrapper := t.TempDir()
	published := filepath.Join(wrapper, "published")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	// The actual Git child completes update-ref before the wrapper blocks.
	// Later updates run normally; cancellation kills only the idle wrapper.
	script := "#!/bin/sh\ncase \"$*\" in *update-ref*) if [ ! -e " + quote(published) + " ]; then " + quote(realGit) + " \"$@\"; status=$?; if [ \"$status\" -ne 0 ]; then exit \"$status\"; fi; printf published > " + quote(published) + "; while :; do :; done; fi ;; esac\nexec " + quote(realGit) + " \"$@\"\n"
	if os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0700) != nil {
		t.Fatal("Git publication gate unavailable")
	}
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := LocalProjectSetupRequest{Project: "lab", ProjectID: "lab", StorageRef: "data", Ref: "refs/heads/main", Confirm: true, Initialize: true}
	done := make(chan error, 1)
	go func() { _, err := SetupLocalProject(ctx, src, req); done <- err }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("initializer did not join")
			}
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(published); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initializer did not publish first ref")
		}
		time.Sleep(time.Millisecond)
	}
	register := req
	register.Initialize = false
	if _, err := SetupLocalProject(context.Background(), src, register); err != nil {
		t.Fatal("published store could not be concurrently registered")
	}
	eff, err := config.LoadEffective(src, config.Overrides{})
	if err != nil {
		t.Fatal("registered config unavailable")
	}
	host, err := NewLocalProjects(context.Background(), eff.Config.LocalProjects, func(ref string) (string, error) { return config.LocalProjectStorePath(src, ref) })
	if err != nil {
		t.Fatal("registered shared service unavailable")
	}
	status, err := host.Status(context.Background())
	if err != nil || len(status.Projects) != 1 {
		t.Fatal("registered source status unavailable")
	}
	result, err := host.Mutate(context.Background(), LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "create_issue", Title: "Retained issue", Body: "Published evidence", ExpectedHead: status.Projects[0].Commit, IdempotencyKey: "retained", WriteMode: "local"}})
	if err != nil || result.Entity.Metadata.ID != "ISSUE-1" {
		t.Fatal("concurrent registered data publication failed")
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if err == nil {
			t.Fatal("interrupted initializer unexpectedly acknowledged")
		}
	case <-time.After(time.Second):
		t.Fatal("interrupted initializer did not join")
	}
	path, _ := config.LocalProjectStorePath(src, "data")
	store, err := localgit.Open(context.Background(), path, localgit.Limits{})
	if err != nil {
		t.Fatal("initialization cleanup deleted concurrently registered Git data")
	}
	snapshot, err := store.Snapshot(context.Background(), "refs/heads/main")
	if err != nil {
		t.Fatal("registered project authority disappeared")
	}
	got, err := snapshot.Get(context.Background(), localgit.Issue, "ISSUE-1")
	if err != nil || got.Citation != result.Entity.Citation || got.Body != result.Entity.Body {
		t.Fatal("registered entity or receipt result was lost")
	}
}

func TestLocalProjectSetupConfigPublicationStaysWithinOwnReadBound(t *testing.T) {
	src := localSetupSource{root: t.TempDir()}
	src.path = filepath.Join(src.root, "selected.yaml")
	original := []byte("#" + strings.Repeat("x", (1<<20)-64) + "\nformat: json\n")
	if os.WriteFile(src.path, original, 0600) != nil {
		t.Fatal("bounded config fixture unavailable")
	}
	req := LocalProjectSetupRequest{Project: "lab", ProjectID: "lab", StorageRef: "data", Ref: "refs/heads/main", Confirm: true, Initialize: true}
	_, err := SetupLocalProject(context.Background(), src, req)
	after, readErr := os.ReadFile(src.path)
	if readErr != nil {
		t.Fatal("trusted config disappeared")
	}
	if err != nil {
		if !bytes.Equal(after, original) {
			t.Fatal("rejected candidate changed trusted config")
		}
		return
	}
	if len(after) > 1<<20 {
		t.Fatal("setup published YAML beyond its own subsequent read bound")
	}
	req.Initialize = false
	if _, err = SetupLocalProject(context.Background(), src, req); err != nil {
		t.Fatal("successful setup could not replay unchanged registration")
	}
}
