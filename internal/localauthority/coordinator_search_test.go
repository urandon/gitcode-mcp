package localauthority

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestCoordinatorSearchResolvesProjectDefaultAndExplicitOverride(t *testing.T) {
	for _, mode := range []string{"hybrid", "full_text"} {
		t.Run(mode, func(t *testing.T) {
			r, err := newRegistry(privateTestDirectory(t))
			if err != nil {
				t.Fatal("fixture registry failed")
			}
			dir := privateTestDirectory(t)
			m := testManifest()
			m.Search.Default = mode
			s, err := Initialize(context.Background(), r, dir, m)
			if err != nil {
				t.Fatal("configured default fixture failed")
			}
			citation := createIssue(t, s, "synthetic-default-fixture").Citation
			if s.Close() != nil {
				t.Fatal("fixture quiesce failed")
			}
			p, err := r.PrepareRuntime()
			if err != nil {
				t.Fatal("fixture preparation failed")
			}
			defer p.Close()
			c, err := NewStrictCoordinator(context.Background(), p, []ProjectBinding{{testProject, dir}})
			if err != nil {
				t.Fatal("configured default runtime failed")
			}
			defer c.Close(context.Background())
			for _, requested := range []string{"", "hybrid", "full_text"} {
				result, err := c.Search(context.Background(), SearchRequest{ProjectID: testProject, Query: "synthetic task", Mode: requested, Limit: 8})
				want := requested
				if want == "" {
					want = mode
				}
				if err != nil || result.Validate() != nil || result.RequestedMode != want || result.EffectiveMode != "full_text" || len(result.Hits) != 1 || result.Hits[0].Citation != citation {
					t.Fatal("configured search intent was ignored or fabricated")
				}
				if (result.FallbackReason == "policy_denied") != (want == "hybrid") {
					t.Fatal("explicit full-text was reported as semantic fallback")
				}
				body, _ := json.Marshal(result)
				var decoded SearchResult
				if authority.DecodeJSON(body, &decoded) != nil || decoded.Validate() != nil {
					t.Fatal("closed search DTO failed strict round trip")
				}
			}
		})
	}
}

func TestCoordinatorIssueSearchDoesNotHydrateDamagedDocuments(t *testing.T) {
	s, r, dir := readyDocumentStore(t)
	issue := createIssue(t, s, "synthetic-independent-search")
	doc, err := s.MutateDocument(context.Background(), documentRequest(t, "document.create", "synthetic-damaged-search-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# Exact Git search bytes"), testActor)
	if err != nil {
		t.Fatal("document fixture failed")
	}
	oid := doc.Result.Locator.BlobOID
	if os.Rename(filepath.Join(dir, knowledgeDirectoryName, "objects", oid[:2], oid[2:]), filepath.Join(dir, "retained-blob")) != nil || s.Close() != nil {
		t.Fatal("missing-blob fixture failed")
	}
	p, err := r.PrepareRuntime()
	if err != nil {
		t.Fatal("fixture preparation failed")
	}
	defer p.Close()
	c, err := NewStrictCoordinator(context.Background(), p, []ProjectBinding{{testProject, dir}})
	if err != nil {
		t.Fatal("Git-only damage blocked tracker admission")
	}
	defer c.Close(context.Background())
	result, err := c.Search(context.Background(), SearchRequest{ProjectID: testProject, Kind: authority.Issue, Query: "synthetic task", Limit: 8})
	if err != nil || result.Validate() != nil || len(result.Hits) != 1 || result.Hits[0].Citation != issue.Citation || result.Scanned != 2 {
		t.Fatal("issue scope hydrated unrelated damaged Git content")
	}
	if _, err = c.Search(context.Background(), SearchRequest{ProjectID: testProject, Kind: authority.Document, Query: "Git", Limit: 8}); err == nil {
		t.Fatal("document scope silently skipped missing authoritative Git bytes")
	}
	read, err := c.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, Operation: "get", EntityID: issue.Citation.EntityID, Revision: 1})
	if err != nil || len(read.Entities) != 1 {
		t.Fatal("Git-only damage blocked canonical issue read")
	}
	_, err = c.Mutate(context.Background(), request(t, "issue.create", "synthetic-independent-after-search", authority.IssuePayload{Title: "Intact tracker", State: "open"}), testActor)
	if err != nil {
		t.Fatal("failed dependent document search fenced unrelated tracker writes")
	}
}

func TestCoordinatorSearchContinuationFencesResolvedModeAndKind(t *testing.T) {
	s, r, dir := testStore(t)
	for i := 0; i < lexicalEntityBudget+1; i++ {
		createIssue(t, s, fmt.Sprintf("synthetic-core-cursor-%d", i))
	}
	if s.Close() != nil {
		t.Fatal("fixture quiesce failed")
	}
	p, err := r.PrepareRuntime()
	if err != nil {
		t.Fatal("fixture preparation failed")
	}
	defer p.Close()
	c, err := NewStrictCoordinator(context.Background(), p, []ProjectBinding{{testProject, dir}})
	if err != nil {
		t.Fatal("fixture core admission failed")
	}
	defer c.Close(context.Background())
	req := SearchRequest{ProjectID: testProject, Kind: authority.Issue, Query: "synthetic task", Limit: 8}
	first, err := c.Search(context.Background(), req)
	if err != nil || !first.Partial || first.Scanned != lexicalEntityBudget || first.NextCursor == "" {
		t.Fatal("bounded core search hid its continuation")
	}
	req.Cursor = first.NextCursor
	wrong := req
	wrong.Mode = "full_text"
	if _, err = c.Search(context.Background(), wrong); err == nil {
		t.Fatal("cursor retargeted requested/effective mode")
	}
	wrong = req
	wrong.Kind = authority.Document
	if _, err = c.Search(context.Background(), wrong); err == nil {
		t.Fatal("cursor retargeted corpus kind")
	}
	last, err := c.Search(context.Background(), req)
	if err != nil || last.Sequence != first.Sequence || last.Partial || last.Scanned != 1 || len(last.Hits) != 1 {
		t.Fatal("bounded core continuation lost its exact snapshot")
	}
	// Even the internal physical snapshot cursor binds the hydration scope.
	body, _ := base64.RawURLEncoding.DecodeString(req.Cursor)
	var outer coordinatorSearchCursor
	if authority.DecodeJSON(body, &outer) != nil {
		t.Fatal("fixture outer cursor failed")
	}
	body, _ = base64.RawURLEncoding.DecodeString(outer.Lexical)
	var inner lexicalCursor
	if authority.DecodeJSON(body, &inner) != nil {
		t.Fatal("fixture lexical cursor failed")
	}
	_, err = c.projects[testProject].store.Snapshot(context.Background(), SnapshotRequest{ProjectID: testProject, Kind: authority.Document, Limit: 1, Cursor: inner.Snapshot})
	if err == nil {
		t.Fatal("physical snapshot cursor retargeted hydration scope")
	}
}
