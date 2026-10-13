package localauthority

import (
	"bytes"
	"os"
	"path/filepath"

	"gitcode-mcp/internal/authority"
)

// Verify the exact immutable candidate and the single-document delta against
// its reserved parent. Every active blob is checked; a tree name alone does not
// establish that the authoritative document bytes are present and intact.
func (g knowledgeRepository) verifyDocumentEvidence(o documentOperation) error {
	if err := g.checkpoint(""); err != nil {
		return err
	}
	commit, err := g.readObject(o.commitOID(), "commit")
	if err != nil {
		return err
	}
	expected, err := knowledgeCommit(o.Root, o.Metadata.ExpectedRefOID, o.ID)
	if err != nil || !bytes.Equal(commit.data, expected.data) {
		return Error{KnowledgeCorrupt}
	}
	root, err := g.readObject(o.Root, "tree")
	if err != nil {
		return err
	}
	canonical, err := knowledgeRoot(o.Documents)
	if err != nil || !bytes.Equal(root.data, canonical.data) {
		return Error{KnowledgeCorrupt}
	}
	tree, err := g.readObject(o.Documents, "tree")
	if err != nil {
		return err
	}
	entries, err := decodeDocumentTree(tree.data)
	if err != nil {
		return err
	}
	prior, err := g.treeAt(o.Metadata.ExpectedRefOID)
	if err != nil {
		return err
	}
	if o.Operation == "document.create" {
		if prior[o.Entity] != "" {
			return Error{KnowledgeCorrupt}
		}
	} else if prior[o.Entity] == "" {
		return Error{KnowledgeCorrupt}
	}
	if o.Metadata.Result.State == "tombstone" {
		delete(prior, o.Entity)
	} else {
		prior[o.Entity] = o.Metadata.Result.Locator.BlobOID
	}
	want, err := documentTree(prior)
	if err != nil || !bytes.Equal(want.data, tree.data) {
		return Error{KnowledgeCorrupt}
	}
	for id, oid := range entries {
		if err := g.checkpoint("knowledge_evidence_blob"); err != nil {
			return err
		}
		blob, err := g.readObject(oid, "blob")
		if err != nil {
			return err
		}
		if id == o.Entity {
			locator := o.Metadata.Result.Locator
			if locator == nil || hash(blob.data) != locator.SHA256 || int64(len(blob.data)) != locator.ByteLength {
				return Error{KnowledgeCorrupt}
			}
		}
	}
	return g.checkpoint("")
}

func (g knowledgeRepository) syncDocumentEvidence(o documentOperation) error {
	if err := g.verifyDocumentEvidence(o); err != nil {
		return err
	}
	objects := [][2]string{{o.Documents, "tree"}, {o.Root, "tree"}, {o.commitOID(), "commit"}}
	if o.Metadata.Result.State == "live" {
		objects = append(objects, [2]string{o.Metadata.Result.Locator.BlobOID, "blob"})
	}
	for _, pair := range objects {
		if err := g.checkpoint(""); err != nil {
			return err
		}
		object, err := g.readObject(pair[0], pair[1])
		if err != nil {
			return err
		}
		if err = g.writeObject(object); err != nil {
			return err
		}
	}
	return nil
}

// Only trusted original-intent completion owners call this. Equal current/lock
// OIDs are insufficient without original SQL identity + candidate/pin proof.
func (g knowledgeRepository) publishJournalRef(o documentOperation, head bool) error {
	if err := g.verifyDocumentEvidence(o); err != nil {
		return err
	}
	name := filepath.Join("pins", string(o.ID))
	expected := authority.EmptyKnowledgeRef
	if head {
		if o.Phase != "bound" {
			return Error{DocumentRecoveryRequired}
		}
		pin, err := g.readRefCheckpoint(name, "knowledge_head_pin_admission")
		if err != nil {
			return recoveryRefError(err)
		}
		if pin != o.commitOID() {
			return Error{DocumentRecoveryRequired}
		}
		name, expected = filepath.Join("heads", "knowledge"), o.Metadata.ExpectedRefOID
	}
	current, err := g.readRef(name)
	if err != nil {
		return err
	}
	lock, err := g.readRef(name + ".lock")
	if err != nil {
		return err
	}
	if current != authority.EmptyKnowledgeRef && lock != authority.EmptyKnowledgeRef {
		if lock != o.commitOID() || current != expected && current != o.commitOID() {
			return Error{DocumentRecoveryRequired}
		}
		path := filepath.Join(g.directory, "refs", name)
		f, err := openExistingFile(path + ".lock")
		if err != nil {
			return Error{KnowledgeCorrupt}
		}
		err = syncFile(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		if err = g.inject("knowledge_owned_ref_file"); err != nil {
			return err
		}
		actual, err := g.readRef(name)
		if err != nil {
			return recoveryRefError(err)
		}
		if actual != current {
			return Error{DocumentRecoveryRequired}
		}
		if err := g.verifyPhysical(); err != nil {
			return recoveryRefError(err)
		}
		if os.Rename(path+".lock", path) != nil {
			return Error{DocumentRecoveryRequired}
		}
		if err = g.inject("knowledge_owned_ref_rename"); err != nil {
			return err
		}
	}
	if err := g.checkpoint(""); err != nil {
		return err
	}
	return g.publishRef(name, expected, o.commitOID())
}
