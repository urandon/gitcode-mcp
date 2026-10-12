package localauthority

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestKnowledgeMetadataContentFreeOriginalFingerprint(t *testing.T) {
	const sentinel = "document-byte-sentinel-not-a-title"
	actor := Actor{ID: "synthetic-local-actor"}
	prior := map[authority.UUID]string{}
	parent := authority.EmptyKnowledgeRef
	for i, operation := range []string{"document.create", "document.update", "document.tombstone"} {
		request := authority.DocumentMutationRequest{Schema: authority.DocumentContractSchema,
			Intent:    authority.DocumentWriteIntent{ProjectID: testProject, WriteMode: "local", IdempotencyKey: "synthetic-original-key", ExpectedRefOID: parent},
			Operation: operation}
		var markdown []byte
		if i > 0 {
			request.Intent.EntityID, request.Intent.ExpectedRevision = knowledgeTestEntity, uint64(i)
		}
		if operation == "document.tombstone" {
			request.Payload = json.RawMessage(`{}`)
		} else {
			markdown = []byte(sentinel + strings.Repeat("!", i))
			request.Payload, _ = json.Marshal(authority.DocumentPayload{Title: "Protected metadata title", Markdown: string(markdown)})
		}
		candidate, err := planKnowledgeCandidate(prior, parent, knowledgeTestOperation, knowledgeTestEntity, markdown, operation == "document.tombstone")
		if err != nil {
			t.Fatal("metadata candidate fixture failed")
		}
		metadata, err := documentMetadataForCandidate(request, knowledgeTestEntity, knowledgeTestOperation, uint64(i+1), candidate)
		if err != nil {
			t.Fatal("exact candidate metadata rejected")
		}
		body, _ := json.Marshal(metadata)
		if bytes.Contains(body, []byte(sentinel)) || bytes.Contains(body, []byte(request.Intent.IdempotencyKey)) || bytes.Contains(body, []byte(actor.ID)) {
			t.Fatal("document bytes, original key or actor leaked into locator metadata")
		}
		decoded, err := decodeDocumentMetadata(body)
		if err != nil {
			t.Fatal("canonical content-free metadata rejected")
		}
		direct, err := documentFingerprint(request, actor)
		if err != nil {
			t.Fatal("original intent fingerprint failed")
		}
		reconstructed, err := documentMetadataFingerprint(decoded, actor)
		if err != nil || direct != reconstructed {
			t.Fatal("metadata did not reconstruct original intent without Markdown")
		}
		request.Intent.IdempotencyKey = "another-key"
		newKeyFingerprint, _ := documentFingerprint(request, actor)
		if direct != newKeyFingerprint {
			t.Fatal("key text contaminated intent fingerprint")
		}
		otherActor, _ := documentFingerprint(request, Actor{ID: "another-actor"})
		if direct == otherActor {
			t.Fatal("changed actor replayed the original intent")
		}
		request.Intent.ExpectedRefOID = strings.Repeat("a", 64)
		otherRef, _ := documentFingerprint(request, actor)
		if direct == otherRef {
			t.Fatal("changed ref expectation replayed original intent")
		}
		metadata.ExpectedRefOID = strings.Repeat("a", 64)
		if operation == "document.tombstone" {
			requireKnowledgeError(t, metadata.Validate(), Corrupt)
		}
		for _, malformed := range [][]byte{append([]byte(" "), body...), bytes.Replace(body, []byte(`"schema":1`), []byte(`"schema":2`), 1), bytes.Replace(body, []byte(`"schema":1`), []byte(`"unknown":"synthetic-secret","schema":1`), 1)} {
			_, err := decodeDocumentMetadata(malformed)
			requireKnowledgeError(t, err, Corrupt)
		}
		prior, err = decodeDocumentTree(candidate.documents.data)
		if err != nil {
			t.Fatal("next exact parent tree rejected")
		}
		parent = candidate.commit.oid
	}
}

func TestKnowledgeMetadataRejectsDifferentReservation(t *testing.T) {
	r := authority.DocumentMutationRequest{Schema: authority.DocumentContractSchema,
		Intent:    authority.DocumentWriteIntent{ProjectID: testProject, WriteMode: "local", IdempotencyKey: "synthetic-create", ExpectedRefOID: authority.EmptyKnowledgeRef},
		Operation: "document.create", Payload: json.RawMessage(`{"title":"Example","markdown":"exact fixture"}`)}
	c, _ := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, knowledgeTestOperation, knowledgeTestEntity, []byte("exact fixture"), false)
	for _, mutate := range []func(*knowledgeCandidate){
		func(c *knowledgeCandidate) { c.contentHash = strings.Repeat("a", 64) },
		func(c *knowledgeCandidate) { c.byteLength++ },
		func(c *knowledgeCandidate) { c.root.oid = strings.Repeat("a", 64) },
		func(c *knowledgeCandidate) { c.blob.oid = strings.Repeat("a", 64) },
		func(c *knowledgeCandidate) { c.commit.oid = strings.Repeat("a", 64) },
		func(c *knowledgeCandidate) { c.documents.oid = strings.Repeat("a", 64) },
		func(c *knowledgeCandidate) { c.commit.data = []byte("foreign commit") },
	} {
		copy := c
		mutate(&copy)
		_, err := documentMetadataForCandidate(r, knowledgeTestEntity, knowledgeTestOperation, 1, copy)
		requireKnowledgeError(t, err, Corrupt)
	}
	_, err := documentMetadataForCandidate(r, knowledgeTestEntity, knowledgeTestOperation, 2, c)
	requireKnowledgeError(t, err, Corrupt)
}
