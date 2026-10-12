package localauthority

import (
	"bytes"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"gitcode-mcp/internal/authority"
)

// Algorithm 1 uses owned SHA256 loose objects only. These fixed limits and
// encodings are durable format, not configurable Git behavior. Object bodies
// are transient and must never be serialized into tracker rows or diagnostics.
const knowledgeAlgorithm = 1
const maxActiveDocuments = 4096
const maxRetainedKnowledgePins = 64
const maxKnowledgeTreeBytes = maxActiveDocuments * (len("100644 ") + 36 + len(".md") + 1 + 32)
const maxKnowledgeCommitBytes = 512
const maxKnowledgeBlobBytes = 256 << 10
const knowledgeIdentity = "Local Knowledge <knowledge@example.invalid> 0 +0000"
const knowledgeConfig = "[core]\n\trepositoryformatversion = 1\n\tbare = true\n[extensions]\n\tobjectformat = sha256\n"
const knowledgeHEAD = "ref: refs/heads/knowledge\n"

type knowledgeObject struct {
	kind string
	data []byte
	oid  string
}

func newKnowledgeObject(kind string, data []byte) (knowledgeObject, error) {
	limit := 0
	switch kind {
	case "blob":
		limit = maxKnowledgeBlobBytes
	case "tree":
		limit = maxKnowledgeTreeBytes
	case "commit":
		limit = maxKnowledgeCommitBytes
	default:
		return knowledgeObject{}, Error{KnowledgeCorrupt}
	}
	if len(data) > limit {
		return knowledgeObject{}, Error{Capacity}
	}
	frame := make([]byte, 0, len(kind)+24+len(data))
	frame = append(frame, kind...)
	frame = append(frame, ' ')
	frame = strconv.AppendInt(frame, int64(len(data)), 10)
	frame = append(frame, 0)
	frame = append(frame, data...)
	return knowledgeObject{kind: kind, data: append([]byte(nil), data...), oid: hash(frame)}, nil
}

func knowledgeOIDValid(oid string, allowEmpty bool) bool {
	if len(oid) != 64 || oid != strings.ToLower(oid) || !allowEmpty && oid == authority.EmptyKnowledgeRef {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}

func documentTree(entries map[authority.UUID]string) (knowledgeObject, error) {
	if len(entries) > maxActiveDocuments {
		return knowledgeObject{}, Error{Capacity}
	}
	ids := make([]string, 0, len(entries))
	for id, oid := range entries {
		if !id.Valid() || !knowledgeOIDValid(oid, false) {
			return knowledgeObject{}, Error{KnowledgeCorrupt}
		}
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	var body []byte
	for _, id := range ids {
		body = append(body, "100644 "...)
		body = append(body, id...)
		body = append(body, ".md\x00"...)
		oid, _ := hex.DecodeString(entries[authority.UUID(id)])
		body = append(body, oid...)
	}
	return newKnowledgeObject("tree", body)
}

func decodeDocumentTree(body []byte) (map[authority.UUID]string, error) {
	if len(body) > maxKnowledgeTreeBytes {
		return nil, Error{KnowledgeCorrupt}
	}
	entries := map[authority.UUID]string{}
	for offset := 0; offset < len(body); {
		const prefix = "100644 "
		const recordBytes = len(prefix) + 36 + len(".md") + 1 + 32
		if len(body)-offset < recordBytes || string(body[offset:offset+len(prefix)]) != prefix {
			return nil, Error{KnowledgeCorrupt}
		}
		id := authority.UUID(body[offset+len(prefix) : offset+len(prefix)+36])
		boundary := offset + len(prefix) + 36
		if !id.Valid() || string(body[boundary:boundary+4]) != ".md\x00" {
			return nil, Error{KnowledgeCorrupt}
		}
		oid := hex.EncodeToString(body[boundary+4 : offset+recordBytes])
		if !knowledgeOIDValid(oid, false) || entries[id] != "" {
			return nil, Error{KnowledgeCorrupt}
		}
		entries[id] = oid
		offset += recordBytes
	}
	canonical, err := documentTree(entries)
	if err != nil || !bytes.Equal(canonical.data, body) {
		return nil, Error{KnowledgeCorrupt}
	}
	return entries, nil
}

func knowledgeRoot(treeOID string) (knowledgeObject, error) {
	if !knowledgeOIDValid(treeOID, false) {
		return knowledgeObject{}, Error{KnowledgeCorrupt}
	}
	raw, _ := hex.DecodeString(treeOID)
	return newKnowledgeObject("tree", append([]byte("40000 documents\x00"), raw...))
}

func decodeKnowledgeRoot(body []byte) (string, error) {
	const prefix = "40000 documents\x00"
	if len(body) != len(prefix)+32 || string(body[:len(prefix)]) != prefix {
		return "", Error{KnowledgeCorrupt}
	}
	oid := hex.EncodeToString(body[len(prefix):])
	if !knowledgeOIDValid(oid, false) {
		return "", Error{KnowledgeCorrupt}
	}
	return oid, nil
}

func knowledgeCommit(treeOID, parentOID string, operation authority.UUID) (knowledgeObject, error) {
	if !knowledgeOIDValid(treeOID, false) || !knowledgeOIDValid(parentOID, true) || !operation.Valid() {
		return knowledgeObject{}, Error{KnowledgeCorrupt}
	}
	body := "tree " + treeOID + "\n"
	if parentOID != authority.EmptyKnowledgeRef {
		body += "parent " + parentOID + "\n"
	}
	body += "author " + knowledgeIdentity + "\ncommitter " + knowledgeIdentity + "\n\noperation " + string(operation) + "\n"
	return newKnowledgeObject("commit", []byte(body))
}

func decodeKnowledgeCommit(body []byte) (treeOID, parentOID string, operation authority.UUID, err error) {
	bad := func() (string, string, authority.UUID, error) { return "", "", "", Error{KnowledgeCorrupt} }
	if len(body) > maxKnowledgeCommitBytes {
		return bad()
	}
	lines := strings.Split(string(body), "\n")
	if len(lines) != 6 && len(lines) != 7 || !strings.HasPrefix(lines[0], "tree ") {
		return bad()
	}
	treeOID = strings.TrimPrefix(lines[0], "tree ")
	parentOID = authority.EmptyKnowledgeRef
	position := 1
	if len(lines) == 7 {
		if !strings.HasPrefix(lines[position], "parent ") {
			return bad()
		}
		parentOID = strings.TrimPrefix(lines[position], "parent ")
		if !knowledgeOIDValid(parentOID, false) {
			return bad()
		}
		position++
	}
	if !strings.HasPrefix(lines[position+3], "operation ") {
		return bad()
	}
	operation = authority.UUID(strings.TrimPrefix(lines[position+3], "operation "))
	canonical, e := knowledgeCommit(treeOID, parentOID, operation)
	if e != nil || !bytes.Equal(canonical.data, body) {
		return bad()
	}
	return treeOID, parentOID, operation, nil
}

// knowledgeCandidate is an ephemeral exact plan. The caller reserves only its
// OIDs and content metadata before persisting objects, then independently binds
// verified durable pin evidence before any ref publication.
type knowledgeCandidate struct {
	blob, documents, root, commit knowledgeObject
	contentHash                   string
	byteLength                    int64
}

func planKnowledgeCandidate(prior map[authority.UUID]string, parent string, operation, entity authority.UUID, markdown []byte, tombstone bool) (knowledgeCandidate, error) {
	var c knowledgeCandidate
	if !entity.Valid() || !operation.Valid() || !knowledgeOIDValid(parent, true) {
		return c, Error{KnowledgeCorrupt}
	}
	if parent == authority.EmptyKnowledgeRef && len(prior) != 0 {
		return c, Error{KnowledgeCorrupt}
	}
	entries := make(map[authority.UUID]string, len(prior)+1)
	for id, oid := range prior {
		if !id.Valid() || !knowledgeOIDValid(oid, false) {
			return c, Error{KnowledgeCorrupt}
		}
		entries[id] = oid
	}
	if tombstone {
		if len(markdown) != 0 || entries[entity] == "" || parent == authority.EmptyKnowledgeRef {
			return c, Error{KnowledgeCorrupt}
		}
		delete(entries, entity)
	} else {
		if len(markdown) < 1 {
			return c, authority.Error{Code: authority.InvalidContract}
		}
		var err error
		c.blob, err = newKnowledgeObject("blob", markdown)
		if err != nil {
			return knowledgeCandidate{}, err
		}
		c.contentHash, c.byteLength = hash(markdown), int64(len(markdown))
		entries[entity] = c.blob.oid
	}
	var err error
	if c.documents, err = documentTree(entries); err != nil {
		return knowledgeCandidate{}, err
	}
	if c.root, err = knowledgeRoot(c.documents.oid); err != nil {
		return knowledgeCandidate{}, err
	}
	if c.commit, err = knowledgeCommit(c.root.oid, parent, operation); err != nil {
		return knowledgeCandidate{}, err
	}
	return c, nil
}
