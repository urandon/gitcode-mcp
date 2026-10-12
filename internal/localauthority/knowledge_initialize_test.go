package localauthority

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func TestKnowledgeExplicitSetupAndOriginalReplay(t *testing.T) {
	s, registry, directory := testStore(t)
	r, err := s.SetupKnowledge(context.Background(), testProject, "synthetic-explicit-setup")
	if err != nil || r.Validate() != nil || r.Replayed {
		t.Fatal("explicit setup failed to confirm original qualified store")
	}
	if s.Close() != nil {
		t.Fatal("setup writer quiesce failed")
	}
	s, err = OpenWriter(context.Background(), registry, directory, testProject)
	if err != nil {
		t.Fatal("explicit setup did not survive reopen")
	}
	defer s.Close()
	replay, err := s.SetupKnowledge(context.Background(), testProject, "synthetic-explicit-setup")
	if err != nil || !replay.Replayed || replay.InstanceID != r.InstanceID || replay.ProjectID != r.ProjectID || replay.Durability != r.Durability {
		t.Fatal("original setup key changed identity or durability")
	}
	_, err = s.SetupKnowledge(context.Background(), testProject, "synthetic-different-setup-key")
	var conflict authority.Error
	if !errors.As(err, &conflict) || conflict.Code != authority.Conflict {
		t.Fatal("different key replaced original setup")
	}
	status, err := s.Status(context.Background())
	if err != nil || status.AuthorityGeneration != 0 {
		t.Fatal("setup invented a tracker mutation")
	}
}

func TestKnowledgeSetupPreflightNoEffects(t *testing.T) {
	legacy, _, _ := legacyAuthorityFixture(t)
	_, err := legacy.SetupKnowledge(context.Background(), testProject, "synthetic-legacy-setup")
	requireKnowledgeError(t, err, KnowledgeMigrationRequired)
	if _, err := os.Lstat(filepath.Join(legacy.directory, knowledgeDirectoryName)); !os.IsNotExist(err) {
		t.Fatal("legacy setup created Git state")
	}
	s, _, _ := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.SetupKnowledge(ctx, testProject, "synthetic-cancelled-setup")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("preflight cancellation changed classification")
	}
	for _, request := range []struct {
		project authority.UUID
		key     string
	}{{testProject, ""}, {knowledgeTestEntity, "synthetic-wrong-project"}} {
		if _, err := s.SetupKnowledge(context.Background(), request.project, request.key); err == nil {
			t.Fatal("invalid setup preflight accepted")
		}
	}
	root := filepath.Join(s.directory, knowledgeDirectoryName)
	if os.Mkdir(root, 0700) != nil || os.WriteFile(filepath.Join(root, "foreign"), []byte("preserved"), 0600) != nil {
		t.Fatal("foreign preflight fixture failed")
	}
	_, err = s.SetupKnowledge(context.Background(), testProject, "synthetic-foreign-setup")
	requireKnowledgeError(t, err, KnowledgeCorrupt)
	var reserved int
	if s.db.QueryRow("SELECT count(*) FROM knowledge_setup").Scan(&reserved) != nil || reserved != 0 {
		t.Fatal("failed preflight consumed an original key")
	}
	body, _ := os.ReadFile(filepath.Join(root, "foreign"))
	if string(body) != "preserved" {
		t.Fatal("foreign repository was modified")
	}
}

func TestKnowledgeSetupProcessHelper(t *testing.T) {
	if os.Getenv("GCM_KNOWLEDGE_SETUP_CHILD") != "1" {
		return
	}
	r, err := newRegistry(os.Getenv("GCM_KNOWLEDGE_SETUP_REGISTRY"))
	if err != nil {
		t.Fatal("child setup registry unavailable")
	}
	s, err := OpenWriter(context.Background(), r, os.Getenv("GCM_KNOWLEDGE_SETUP_DIRECTORY"), testProject)
	if err != nil {
		t.Fatal("child setup writer unavailable")
	}
	defer s.Close()
	cut, _ := strconv.Atoi(os.Getenv("GCM_KNOWLEDGE_SETUP_CUT"))
	seen := 0
	s.fault = func(at string) error {
		if at == os.Getenv("GCM_KNOWLEDGE_SETUP_PHASE") {
			seen++
			if seen == cut {
				os.Exit(77)
			}
		}
		return nil
	}
	_, err = s.SetupKnowledge(context.Background(), testProject, "synthetic-crash-setup")
	if err != nil {
		t.Fatal("child setup stopped before expected boundary")
	}
	t.Fatal("child setup did not reach requested boundary")
}

func TestKnowledgeSetupAbruptExitRetainsOriginalIdentity(t *testing.T) {
	for _, phase := range []struct {
		name        string
		occurrences int
	}{
		{"knowledge_setup_reserved", 1}, {"knowledge_setup_unbound_directory", 1}, {"knowledge_setup_bound", 1},
		{"knowledge_setup_directory", 4}, {"knowledge_setup_metadata", 2}, {"knowledge_setup_layout", 1},
		{"knowledge_setup_ready", 1}, {"database_sync", 3}, {"wal_sync", 3}, {"directory_sync", 3},
	} {
		for cut := 1; cut <= phase.occurrences; cut++ {
			t.Run(phase.name+"-"+strconv.Itoa(cut), func(t *testing.T) {
				s, registry, directory := testStore(t)
				if s.Close() != nil {
					t.Fatal("crash fixture writer quiesce failed")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKnowledgeSetupProcessHelper$")
				cmd.Env = append(os.Environ(), "GCM_KNOWLEDGE_SETUP_CHILD=1", "GCM_KNOWLEDGE_SETUP_REGISTRY="+registry.root,
					"GCM_KNOWLEDGE_SETUP_DIRECTORY="+directory, "GCM_KNOWLEDGE_SETUP_PHASE="+phase.name, "GCM_KNOWLEDGE_SETUP_CUT="+strconv.Itoa(cut))
				var exited *exec.ExitError
				if !errors.As(cmd.Run(), &exited) || exited.ExitCode() != 77 {
					t.Fatal("setup subprocess did not reach the exact requested boundary")
				}
				opened, err := OpenWriter(context.Background(), registry, directory, testProject)
				if err != nil {
					t.Fatal("original setup SQL did not survive abrupt exit")
				}
				defer opened.Close()
				original, err := loadKnowledgeSetup(context.Background(), opened.db, testProject, opened.instance, opened.directory)
				if err != nil {
					t.Fatal("admitted original setup identity missing")
				}
				result, err := opened.SetupKnowledge(context.Background(), testProject, "synthetic-crash-setup")
				if phase.name == "knowledge_setup_unbound_directory" {
					requireKnowledgeError(t, err, KnowledgeInitializationPending)
					retained, e := loadKnowledgeSetup(context.Background(), opened.db, testProject, opened.instance, opened.directory)
					if e != nil || retained != original || retained.Physical != "" {
						t.Fatal("ambiguous unbound directory was adopted or original identity lost")
					}
					createIssue(t, opened, "synthetic-tracker-with-unbound-setup")
					return
				}
				if err != nil || result.Validate() != nil || !result.Replayed || result.InstanceID != original.Instance {
					t.Fatal("same-key setup recovery invented a new identity or weakened acknowledgement")
				}
			})
		}
	}
}
