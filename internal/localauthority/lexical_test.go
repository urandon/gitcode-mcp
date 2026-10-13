package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"gitcode-mcp/internal/authority"
)

func TestLexicalRealTypedUnicodeSearchWithoutProjection(t *testing.T) {
	s, registry, directory := readyDocumentStore(t)
	ctx := context.Background()
	r := mustMutate(t, s, request(t, "issue.create", "lexical-issue", authority.IssuePayload{Title: "Наблюдение", Body: "Персистентный локальный индекс; индекс", State: "open"}))
	_, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "lexical-document", authority.EmptyKnowledgeRef, authority.Citation{}, "# Локальный индекс\nGit-only research bytes."), testActor)
	if err != nil {
		t.Fatal("document fixture failed")
	}
	if s.Close() != nil {
		t.Fatal("fixture close failed")
	}
	ro, err := OpenReadOnly(ctx, directory, testProject)
	if err != nil {
		t.Fatal("read-only open failed")
	}
	defer ro.Close()
	result, err := ro.LexicalSearch(ctx, LexicalRequest{ProjectID: testProject, Query: "ЛОКАЛЬНЫЙ индекс", Limit: 10})
	if err != nil || result.Partial || result.Scanned != 2 || len(result.Hits) != 2 || result.Hits[0].Citation != r.Citation || result.Hits[0].Score != 3 {
		t.Fatal("real deterministic Unicode content matching failed")
	}
	for _, hit := range result.Hits {
		if !utf8.ValidString(hit.Snippet) || len(hit.Snippet) > 512 || hit.Citation.Validate() != nil || len(hit.ContentDigest) != 64 {
			t.Fatal("invalid citation or snippet")
		}
		view, err := ro.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: hit.Citation.Kind, EntityID: hit.Citation.EntityID, Revision: hit.Citation.Revision, Operation: "get"})
		if err != nil || len(view.Entities) != 1 {
			t.Fatal("search citation was not independently readable")
		}
	}
	result, err = ro.LexicalSearch(ctx, LexicalRequest{ProjectID: testProject, Query: "body", Limit: 10})
	if err != nil || len(result.Hits) != 0 {
		t.Fatal("JSON field names became searchable content")
	}
	// Search is an inspection only: it did not acquire a writer or poison its
	// global physical binding. The existing owner can still be admitted.
	writer, err := OpenWriter(ctx, registry, directory, testProject)
	if err != nil {
		t.Fatal("read-only search changed writer admission")
	}
	defer writer.Close()
}

func TestLexicalBoundedSlicesAndCursorScope(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	for i := 0; i < lexicalEntityBudget+1; i++ {
		mustMutate(t, s, request(t, "issue.create", fmt.Sprintf("lexical-bounded-%d", i), authority.IssuePayload{Title: "Example corpus", Body: "keyword", State: "open"}))
	}
	req := LexicalRequest{ProjectID: testProject, Query: "keyword", Limit: 10}
	first, err := s.LexicalSearch(ctx, req)
	if err != nil || first.Scanned != lexicalEntityBudget || !first.Partial || first.NextCursor == "" || len(first.Hits) != 10 {
		t.Fatal("bounded slice concealed incomplete corpus coverage")
	}
	encoded, _ := json.Marshal(first)
	if len(encoded) > authority.MaxContractBytes || strings.Contains(first.NextCursor, req.Query) {
		t.Fatal("search result bound or content-free cursor failed")
	}
	req.Cursor = first.NextCursor
	last, err := s.LexicalSearch(ctx, req)
	if err != nil || last.Sequence != first.Sequence || last.Scanned != 1 || last.Partial || last.NextCursor != "" || len(last.Hits) != 1 {
		t.Fatal("search continuation lost bounded snapshot")
	}
	for _, mutate := range []func(*LexicalRequest){func(r *LexicalRequest) { r.Query = "different" }, func(r *LexicalRequest) { r.Kind = authority.Document }, func(r *LexicalRequest) { r.ProjectID = "22222222-2222-4222-8222-222222222222" }} {
		wrong := req
		mutate(&wrong)
		if _, err := s.LexicalSearch(ctx, wrong); err == nil {
			t.Fatal("retargeted cursor accepted")
		}
	}
	for _, query := range []string{"", "<>", strings.Repeat("x", 4097), string([]byte{0xff})} {
		if _, err := s.LexicalSearch(ctx, LexicalRequest{ProjectID: testProject, Query: query, Limit: 1}); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.LexicalSearch(cancelled, LexicalRequest{ProjectID: testProject, Query: "keyword", Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal("search lost caller cancellation")
	}
}

func TestLexicalTombstonesAndByteSafeSnippet(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	ctx := context.Background()
	created, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "lexical-doc-create", authority.EmptyKnowledgeRef, authority.Citation{}, strings.Repeat("中", 200)+" keyword "+strings.Repeat("文", 200)), testActor)
	if err != nil {
		t.Fatal("document create failed")
	}
	req := LexicalRequest{ProjectID: testProject, Query: "keyword", Limit: 10}
	before, err := s.LexicalSearch(ctx, req)
	if err != nil || len(before.Hits) != 1 || !utf8.ValidString(before.Hits[0].Snippet) || len(before.Hits[0].Snippet) > 512 || !strings.Contains(before.Hits[0].Snippet, "keyword") {
		t.Fatal("matching multibyte snippet is invalid")
	}
	_, err = s.MutateDocument(ctx, documentRequest(t, "document.tombstone", "lexical-doc-delete", created.Result.Locator.CommitOID, created.Receipt.Citation, ""), testActor)
	if err != nil {
		t.Fatal("document delete failed")
	}
	after, err := s.LexicalSearch(ctx, req)
	if err != nil || len(after.Hits) != 0 || after.Sequence <= before.Sequence {
		t.Fatal("active search retained tombstone")
	}
	history, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Document, EntityID: created.Receipt.Citation.EntityID, Revision: 1, Operation: "get"})
	if err != nil || len(history.Entities) != 1 {
		t.Fatal("search invalidation erased historical citation")
	}
}
