package localauthority

import (
	"bytes"
	"context"

	"gitcode-mcp/internal/authority"
)

// Validate every settled authoritative object, not only the current tip. A
// missing superseded blob is still lost authority and must fence new document
// writes. Only OIDs/digests/lengths are memoized in this bounded transient scan;
// Markdown is read one blob at a time and never retained in a cache or SQL.
func (s *Store) verifySettledKnowledgeHistory(ctx context.Context, g knowledgeRepository) (string, error) {
	rows, err := s.db.QueryContext(ctx, documentOperationQuery+` WHERE phase='settled' ORDER BY (SELECT sequence FROM receipts WHERE receipts.key_hash=document_operations.key_hash)`)
	if err != nil {
		return "", storageError(ctx)
	}
	defer rows.Close()
	type blobProof struct {
		digest string
		length int64
	}
	blobs := map[string]blobProof{}
	previous := authority.EmptyKnowledgeRef
	previousTree := ""
	entries := map[authority.UUID]string{}
	count := 0
	for rows.Next() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		count++
		if count > maxKnowledgeLooseObjects {
			return "", Error{Capacity}
		}
		o, err := scanDocumentOperation(ctx, rows, s.manifest.ID)
		if err != nil {
			return "", s.documentSQLError(err)
		}
		if o.Metadata.ExpectedRefOID != previous {
			return "", Error{KnowledgeCorrupt}
		}
		commit, err := g.readObject(o.commitOID(), "commit")
		if err != nil {
			return "", err
		}
		canonicalCommit, err := knowledgeCommit(o.Root, previous, o.ID)
		if err != nil || !bytes.Equal(commit.data, canonicalCommit.data) {
			return "", Error{KnowledgeCorrupt}
		}
		root, err := g.readObject(o.Root, "tree")
		if err != nil {
			return "", err
		}
		canonicalRoot, err := knowledgeRoot(o.Documents)
		if err != nil || !bytes.Equal(root.data, canonicalRoot.data) {
			return "", Error{KnowledgeCorrupt}
		}
		next := entries
		if o.Documents != previousTree {
			tree, err := g.readObject(o.Documents, "tree")
			if err != nil {
				return "", err
			}
			next, err = decodeDocumentTree(tree.data)
			if err != nil {
				return "", err
			}
		}
		if o.Operation == "document.create" && entries[o.Entity] != "" || o.Operation != "document.create" && entries[o.Entity] == "" {
			return "", Error{KnowledgeCorrupt}
		}
		// Compare the exact single-entry delta without duplicating a tree's
		// byte payload or building an unbounded history of UUID maps.
		wantCount := len(entries)
		if o.Operation == "document.create" {
			wantCount++
		}
		if o.Operation == "document.tombstone" {
			wantCount--
		}
		if len(next) != wantCount {
			return "", Error{KnowledgeCorrupt}
		}
		for id, oid := range entries {
			if id != o.Entity && next[id] != oid {
				return "", Error{KnowledgeCorrupt}
			}
		}
		if o.Metadata.Result.State == "tombstone" {
			if next[o.Entity] != "" {
				return "", Error{KnowledgeCorrupt}
			}
		} else {
			locator := o.Metadata.Result.Locator
			if next[o.Entity] != locator.BlobOID {
				return "", Error{KnowledgeCorrupt}
			}
			proof, seen := blobs[locator.BlobOID]
			if !seen {
				if len(blobs) >= maxKnowledgeLooseObjects {
					return "", Error{Capacity}
				}
				blob, err := g.readObject(locator.BlobOID, "blob")
				if err != nil {
					return "", err
				}
				proof = blobProof{hash(blob.data), int64(len(blob.data))}
				blobs[locator.BlobOID] = proof
			}
			if proof.digest != locator.SHA256 || proof.length != locator.ByteLength {
				return "", Error{KnowledgeCorrupt}
			}
		}
		previous, previousTree, entries = o.commitOID(), o.Documents, next
	}
	if rows.Err() != nil {
		return "", storageError(ctx)
	}
	return previous, nil
}
