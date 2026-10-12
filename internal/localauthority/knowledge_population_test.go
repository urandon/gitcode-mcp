package localauthority

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func unpublishedKnowledgeFixture(t *testing.T) knowledgeRepository {
	t.Helper()
	s, _, _ := testStore(t)
	k := bindKnowledgeSetupFixture(t, s, reserveKnowledgeSetupFixture(t, s))
	return s.knowledgeRepository(k)
}

func TestKnowledgePopulationBoundPartialLayoutReplays(t *testing.T) {
	for _, phase := range []string{"knowledge_setup_directory", "knowledge_setup_metadata", "knowledge_setup_layout"} {
		for cut := 1; cut <= map[string]int{"knowledge_setup_directory": 4, "knowledge_setup_metadata": 2, "knowledge_setup_layout": 1}[phase]; cut++ {
			g := unpublishedKnowledgeFixture(t)
			injected := errors.New("synthetic population interruption")
			seen := 0
			g.fault = func(at string) error {
				if at == phase {
					seen++
					if seen == cut {
						return injected
					}
				}
				return nil
			}
			if !errors.Is(g.populate(), injected) {
				t.Fatal("known population boundary was not interrupted")
			}
			g.fault = nil
			if g.populate() != nil || g.populate() != nil {
				t.Fatal("exact bound partial layout could not resume idempotently")
			}
			objects, pins, err := g.verifyLayout()
			if err != nil || objects != 0 || pins != 0 {
				t.Fatal("setup created hidden document objects or pins")
			}
		}
	}
}

func TestKnowledgePopulationRefusesForeignStateBeforeNewEffects(t *testing.T) {
	for _, kind := range []string{"unknown", "torn_config", "symlink", "hardlink", "object", "ref"} {
		g := unpublishedKnowledgeFixture(t)
		path := filepath.Join(g.directory, "foreign.txt")
		if kind == "torn_config" || kind == "hardlink" {
			path = filepath.Join(g.directory, "config")
		}
		if kind == "object" {
			if os.Mkdir(filepath.Join(g.directory, "objects"), 0700) != nil {
				t.Fatal("foreign object directory fixture failed")
			}
			path = filepath.Join(g.directory, "objects", "foreign")
		}
		if kind == "ref" {
			for _, directory := range []string{"refs", "refs/heads"} {
				if os.Mkdir(filepath.Join(g.directory, directory), 0700) != nil {
					t.Fatal("foreign ref directory fixture failed")
				}
			}
			path = filepath.Join(g.directory, "refs", "heads", "knowledge")
		}
		body := []byte("synthetic preserved foreign state")
		if kind == "hardlink" {
			body = []byte(knowledgeConfig)
		}
		if kind == "symlink" {
			path = filepath.Join(g.directory, "objects")
			if os.Symlink(t.TempDir(), path) != nil {
				t.Fatal("foreign symlink fixture failed")
			}
		} else if os.WriteFile(path, body, 0600) != nil {
			t.Fatal("foreign population fixture failed")
		}
		if kind == "hardlink" && os.Link(path, filepath.Join(t.TempDir(), "linked-config")) != nil {
			t.Fatal("foreign hardlink fixture failed")
		}
		requireKnowledgeError(t, g.populate(), KnowledgeCorrupt)
		if _, err := os.Lstat(filepath.Join(g.directory, "HEAD")); !os.IsNotExist(err) {
			t.Fatal("foreign-state refusal created metadata before preflight")
		}
		if kind != "symlink" {
			preserved, err := os.ReadFile(path)
			if err != nil || string(preserved) != string(body) {
				t.Fatal("foreign or torn evidence was changed")
			}
		}
	}
}
