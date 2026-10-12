package localauthority

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func knowledgeRepositoryFixture(t *testing.T) knowledgeRepository {
	t.Helper()
	root := filepath.Join(t.TempDir(), "knowledge.git")
	for _, directory := range []string{"", "objects", "refs", "refs/heads", "refs/pins"} {
		if os.Mkdir(filepath.Join(root, directory), 0700) != nil {
			t.Fatal("synthetic owned repository setup failed")
		}
	}
	for name, body := range map[string]string{"config": knowledgeConfig, "HEAD": knowledgeHEAD} {
		if os.WriteFile(filepath.Join(root, name), []byte(body), 0600) != nil {
			t.Fatal("synthetic owned repository metadata failed")
		}
	}
	physical, err := knowledgeDirectoryIdentity(root)
	if err != nil || qualifyFilesystem(root) != nil {
		t.Fatal("qualified synthetic knowledge directory unavailable")
	}
	g := knowledgeRepository{directory: root, physical: physical}
	if _, _, err = g.verifyLayout(); err != nil {
		t.Fatal("empty owned format rejected")
	}
	return g
}

func writeKnowledgeCandidateFixture(t *testing.T, g knowledgeRepository, c knowledgeCandidate) {
	t.Helper()
	objects := []knowledgeObject{c.documents, c.root, c.commit}
	if c.blob.oid != "" {
		objects = append(objects, c.blob)
	}
	for _, object := range objects {
		if g.writeObject(object) != nil {
			t.Fatal("owned candidate publication failed")
		}
	}
}

func TestKnowledgeFilesRoundTripPinnedTreeAndExactRef(t *testing.T) {
	g := knowledgeRepositoryFixture(t)
	c, _ := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, knowledgeTestOperation, knowledgeTestEntity, []byte("# Exact\r\nprivate fixture\n"), false)
	writeKnowledgeCandidateFixture(t, g, c)
	writeKnowledgeCandidateFixture(t, g, c)
	objects, pins, err := g.verifyLayout()
	if err != nil || objects != 4 || pins != 0 {
		t.Fatal("replayed objects changed inventory")
	}
	pin := filepath.Join("pins", string(knowledgeTestOperation))
	if g.publishRef(pin, authority.EmptyKnowledgeRef, c.commit.oid) != nil || g.publishRef(pin, authority.EmptyKnowledgeRef, c.commit.oid) != nil {
		t.Fatal("exact pin did not replay safely")
	}
	if g.publishRef("heads/knowledge", authority.EmptyKnowledgeRef, c.commit.oid) != nil {
		t.Fatal("exact authority ref publication failed")
	}
	entries, err := g.treeAt(c.commit.oid)
	if err != nil || entries[knowledgeTestEntity] != c.blob.oid || len(entries) != 1 {
		t.Fatal("pinned exact tree could not be hydrated")
	}
	update, _ := planKnowledgeCandidate(entries, c.commit.oid, knowledgeTestOperation, knowledgeTestEntity, []byte("changed"), false)
	writeKnowledgeCandidateFixture(t, g, update)
	if g.publishRef("heads/knowledge", c.commit.oid, update.commit.oid) != nil {
		t.Fatal("new authority ref publication failed")
	}
	old, err := g.treeAt(c.commit.oid)
	if err != nil || !reflect.DeepEqual(old, entries) {
		t.Fatal("immutable locator was resolved through current ref")
	}
	prior, _ := g.readRef("heads/knowledge")
	requireKnowledgeError(t, g.publishRef("heads/knowledge", c.commit.oid, strings.Repeat("a", 64)), DocumentRecoveryRequired)
	after, _ := g.readRef("heads/knowledge")
	if prior != after {
		t.Fatal("divergent ref was force updated")
	}
	blob, err := g.readObject(c.blob.oid, "blob")
	if err != nil || !bytes.Equal(blob.data, c.blob.data) {
		t.Fatal("immutable document bytes changed")
	}
	if _, err = g.readObject(c.blob.oid, "tree"); err == nil {
		t.Fatal("wrong object type accepted")
	}
}

func TestKnowledgeFilesRefAndObjectBarrierRecovery(t *testing.T) {
	for _, phase := range []string{"knowledge_object_file", "knowledge_object_directory", "knowledge_ref_file", "knowledge_ref_rename", "knowledge_ref_directory"} {
		t.Run(phase, func(t *testing.T) {
			g := knowledgeRepositoryFixture(t)
			c, _ := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, knowledgeTestOperation, knowledgeTestEntity, []byte("exact recovery fixture"), false)
			if strings.HasPrefix(phase, "knowledge_ref") {
				writeKnowledgeCandidateFixture(t, g, c)
			}
			fired := false
			injected := errors.New("synthetic barrier interruption")
			g.fault = func(at string) error {
				if at == phase && !fired {
					fired = true
					return injected
				}
				return nil
			}
			var err error
			if strings.HasPrefix(phase, "knowledge_ref") {
				err = g.publishRef("heads/knowledge", authority.EmptyKnowledgeRef, c.commit.oid)
			} else {
				err = g.writeObject(c.blob)
			}
			if !fired || !errors.Is(err, injected) {
				t.Fatal("barrier interruption was not observed")
			}
			g.fault = nil
			if strings.HasPrefix(phase, "knowledge_ref") {
				if g.publishRef("heads/knowledge", authority.EmptyKnowledgeRef, c.commit.oid) != nil {
					t.Fatal("original exact ref could not resume")
				}
				ref, err := g.readRef("heads/knowledge")
				if err != nil || ref != c.commit.oid {
					t.Fatal("ref recovery chose another candidate")
				}
			} else if g.writeObject(c.blob) != nil {
				t.Fatal("original exact object could not resume")
			}
		})
	}
}

func TestKnowledgeFilesRejectForeignStateAndPreserveEvidence(t *testing.T) {
	for _, name := range []string{"hooks", "packed-refs", "shallow", "objects/info", "objects/pack", "refs/replace", "worktrees"} {
		t.Run(name, func(t *testing.T) {
			g := knowledgeRepositoryFixture(t)
			if os.Mkdir(filepath.Join(g.directory, name), 0700) != nil {
				t.Fatal("foreign layout fixture failed")
			}
			_, _, err := g.verifyLayout()
			requireKnowledgeError(t, err, KnowledgeCorrupt)
			if _, err := os.Lstat(filepath.Join(g.directory, name)); err != nil {
				t.Fatal("foreign state was deleted")
			}
		})
	}
	g := knowledgeRepositoryFixture(t)
	if os.WriteFile(filepath.Join(g.directory, "config"), []byte(knowledgeConfig+"[core]\n\thooksPath = forbidden\n"), 0600) != nil {
		t.Fatal("foreign config fixture failed")
	}
	_, _, err := g.verifyLayout()
	requireKnowledgeError(t, err, KnowledgeCorrupt)
	for _, mode := range []string{"hardlink", "symlink", "torn", "trailing", "unprivate"} {
		t.Run(mode, func(t *testing.T) {
			g := knowledgeRepositoryFixture(t)
			c, _ := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, knowledgeTestOperation, knowledgeTestEntity, []byte("private object fixture"), false)
			if g.writeObject(c.blob) != nil {
				t.Fatal("object fixture failed")
			}
			path := filepath.Join(g.directory, "objects", c.blob.oid[:2], c.blob.oid[2:])
			original, e := os.ReadFile(path)
			if e != nil {
				t.Fatal("fixture read failed")
			}
			switch mode {
			case "hardlink":
				err = os.Link(path, filepath.Join(t.TempDir(), "linked-object"))
			case "symlink":
				err = os.Rename(path, path+".fixture")
				if err == nil {
					err = os.Symlink(path+".fixture", path)
				}
			case "torn":
				err = os.WriteFile(path, original[:len(original)/2], 0600)
			case "trailing":
				err = os.WriteFile(path, append(original, 0), 0600)
			case "unprivate":
				err = os.Chmod(path, 0644)
			}
			if err != nil {
				t.Fatal("unsafe object fixture failed")
			}
			_, err = g.readObject(c.blob.oid, "blob")
			requireKnowledgeError(t, err, KnowledgeCorrupt)
			requireKnowledgeError(t, g.writeObject(c.blob), KnowledgeCorrupt)
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("uncertain original evidence was deleted")
			}
		})
	}
	for _, value := range []string{"partial", strings.Repeat("b", 64) + "\n"} {
		g := knowledgeRepositoryFixture(t)
		lock := filepath.Join(g.directory, "refs", "heads", "knowledge.lock")
		if os.WriteFile(lock, []byte(value), 0600) != nil {
			t.Fatal("uncertain lock fixture failed")
		}
		requireKnowledgeError(t, g.publishRef("heads/knowledge", authority.EmptyKnowledgeRef, strings.Repeat("a", 64)), DocumentRecoveryRequired)
		body, _ := os.ReadFile(lock)
		if string(body) != value {
			t.Fatal("foreign or partial lock was rewritten")
		}
	}
	g = knowledgeRepositoryFixture(t)
	g.physical = strings.Repeat("a", 64)
	_, _, err = g.verifyLayout()
	requireKnowledgeError(t, err, KnowledgeCorrupt)
}

func TestKnowledgeFilesPairedRefLocksArePreservedAndFenced(t *testing.T) {
	for _, name := range []string{"heads/knowledge", "pins/" + string(knowledgeTestOperation)} {
		for _, equal := range []bool{false, true} {
			t.Run(name+map[bool]string{true: "-equal", false: "-conflicting"}[equal], func(t *testing.T) {
				g := knowledgeRepositoryFixture(t)
				c, _ := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, knowledgeTestOperation, knowledgeTestEntity, []byte("paired lock fixture"), false)
				writeKnowledgeCandidateFixture(t, g, c)
				if g.publishRef(name, authority.EmptyKnowledgeRef, c.commit.oid) != nil {
					t.Fatal("finalized ref fixture failed")
				}
				lockedOID := strings.Repeat("b", 64)
				if equal {
					lockedOID = c.commit.oid
				}
				lock := filepath.Join(g.directory, "refs", name) + ".lock"
				if os.WriteFile(lock, []byte(lockedOID+"\n"), 0600) != nil {
					t.Fatal("paired lock fixture failed")
				}
				_, _, err := g.verifyLayout()
				requireKnowledgeError(t, err, KnowledgeCorrupt)
				for _, expected := range []string{authority.EmptyKnowledgeRef, c.commit.oid} {
					requireKnowledgeError(t, g.publishRef(name, expected, c.commit.oid), DocumentRecoveryRequired)
				}
				final, _ := os.ReadFile(filepath.Join(g.directory, "refs", name))
				pending, _ := os.ReadFile(lock)
				if string(final) != c.commit.oid+"\n" || string(pending) != lockedOID+"\n" {
					t.Fatal("ambiguous paired evidence was changed or removed")
				}
			})
		}
	}
}

func TestKnowledgeFilesInterruptedUpdateNeedsOriginalJournalProof(t *testing.T) {
	g := knowledgeRepositoryFixture(t)
	c, _ := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, knowledgeTestOperation, knowledgeTestEntity, []byte("original"), false)
	writeKnowledgeCandidateFixture(t, g, c)
	if g.publishRef("heads/knowledge", authority.EmptyKnowledgeRef, c.commit.oid) != nil {
		t.Fatal("original ref fixture failed")
	}
	entries, _ := decodeDocumentTree(c.documents.data)
	next, _ := planKnowledgeCandidate(entries, c.commit.oid, knowledgeTestOperation, knowledgeTestEntity, []byte("next"), false)
	writeKnowledgeCandidateFixture(t, g, next)
	injected := errors.New("synthetic update ref interruption")
	g.fault = func(at string) error {
		if at == "knowledge_ref_file" {
			return injected
		}
		return nil
	}
	if !errors.Is(g.publishRef("heads/knowledge", c.commit.oid, next.commit.oid), injected) {
		t.Fatal("update ref interruption was not observed")
	}
	g.fault = nil
	// The basic primitive cannot assert journal ownership just from two OIDs.
	// The subsequent SQL recovery layer must prove the original bound operation
	// before a separate explicit recovery transition can publish this pair.
	requireKnowledgeError(t, g.publishRef("heads/knowledge", c.commit.oid, next.commit.oid), DocumentRecoveryRequired)
	final, _ := g.readRef("heads/knowledge")
	locked, _ := g.readRef("heads/knowledge.lock")
	if final != c.commit.oid || locked != next.commit.oid {
		t.Fatal("interrupted update lost its original ref/lock evidence")
	}
}
