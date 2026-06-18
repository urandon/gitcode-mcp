package index

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIndexPipeline(t *testing.T) {
	ctx := context.Background()
	reader := memoryReader{sources: []SourceRecord{
		fixtureSource("DOC-123", "doc", "docs/design.md", "Design", "ready", "---\ntrack: platform\n---\n# Design\nSee [task](TASK-001) and [missing](MISSING-404).\n## Acceptance\nDone.\n"),
		fixtureSource("TASK-001", "task", "project/task.md", "Task", "ready", "# Task\nstatus: ready\nReferences [[DOC-123]].\n"),
	}}
	writer := &memoryWriter{}
	report, err := FullBuild(ctx, reader, writer)
	if err != nil {
		t.Fatalf("FullBuild returned error: %v", err)
	}
	if report.ProcessedCount != 2 {
		t.Fatalf("ProcessedCount = %d, want 2", report.ProcessedCount)
	}
	if len(writer.derived) != 2 {
		t.Fatalf("derived source count = %d, want 2", len(writer.derived))
	}
	doc := writer.derived["DOC-123"]
	if len(doc.SourceLedgerRows) == 0 || len(doc.TrackRows) == 0 || len(doc.AcceptanceRows) == 0 {
		t.Fatalf("doc projections missing ledger/track/acceptance rows: %+v", doc)
	}
	if len(writer.derived["TASK-001"].TaskBacklogRows) != 1 {
		t.Fatalf("task backlog rows = %d, want 1", len(writer.derived["TASK-001"].TaskBacklogRows))
	}
	if len(doc.Links) != 1 || doc.Links[0].TargetID != "TASK-001" {
		t.Fatalf("resolved doc links = %+v, want TASK-001", doc.Links)
	}
	if len(doc.BrokenLinks) != 1 || doc.BrokenLinks[0].Reason != "unresolved_alias" {
		t.Fatalf("broken doc links = %+v, want unresolved_alias", doc.BrokenLinks)
	}
	stale, err := StaleCheck(ctx, reader, writer)
	if err != nil {
		t.Fatalf("StaleCheck returned error: %v", err)
	}
	if _, err := json.Marshal(stale); err != nil {
		t.Fatalf("StaleReport is not JSON serializable: %v", err)
	}
	if stale.TotalStaleBacklinks == 0 || !contains(stale.AffectedSourceIDs, "DOC-123") {
		t.Fatalf("stale report = %+v, want DOC-123 stale broken-link evidence", stale)
	}
	unchanged := memoryReader{sources: []SourceRecord{
		withPreviousHash(reader.sources[0]),
		withPreviousHash(reader.sources[1]),
	}}
	beforeWrites := writer.replaceCalls
	incremental, err := IncrementalBuild(ctx, unchanged, writer)
	if err != nil {
		t.Fatalf("IncrementalBuild returned error: %v", err)
	}
	if incremental.ProcessedCount != 0 || incremental.SkippedCount != 2 || incremental.RewrittenRowCount != 0 || incremental.BrokenLinkCount != 0 {
		t.Fatalf("incremental report = %+v, want no rewrites", incremental)
	}
	if writer.replaceCalls != beforeWrites {
		t.Fatalf("incremental rewrote derived rows: before %d after %d", beforeWrites, writer.replaceCalls)
	}
}

func TestChunkDeterminism(t *testing.T) {
	source := fixtureSource("DOC-123", "doc", "docs/design.md", "Design", "ready", "---\ncomponent: knowledge-indexer\n---\n# Alpha\nText with [task](TASK-001).\n## Beta\nMore text.\n")
	parsed := ParseSource(source)
	chunks := ChunkSource(source, parsed)
	again := ChunkSource(source, parsed)
	if !reflect.DeepEqual(chunks, again) {
		t.Fatalf("chunks differ across runs\nfirst:  %+v\nsecond: %+v", chunks, again)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunk count = %d, want 3", len(chunks))
	}
	if chunks[1].ByteStart != strings.Index(source.Body, "# Alpha") || chunks[1].LineStart != 4 || !reflect.DeepEqual(chunks[1].HeadingPath, []string{"Alpha"}) {
		t.Fatalf("heading chunk = %+v, want Alpha at original offset/line", chunks[1])
	}
	if len(chunks[1].OutboundLinks) != 1 || chunks[1].OutboundLinks[0] != "TASK-001" {
		t.Fatalf("chunk outbound links = %+v, want TASK-001", chunks[1].OutboundLinks)
	}
	if chunks[1].InheritedMetadata["component"] != "knowledge-indexer" || chunks[1].InheritedMetadata["source_id"] != "DOC-123" {
		t.Fatalf("chunk metadata = %+v", chunks[1].InheritedMetadata)
	}
	for _, chunk := range chunks {
		if chunk.ID == "" || chunk.CitationAnchorID == "" || chunk.NormalizedText == "" {
			t.Fatalf("chunk missing deterministic id/anchor/text: %+v", chunk)
		}
	}
}

func TestCitationAnchors(t *testing.T) {
	ctx := context.Background()
	source := fixtureSource("TASK-001", "task", "project/task.md", "Task", "ready", "# Task\nstatus: ready\n## Acceptance Criteria\n- works\n")
	writer := &memoryWriter{}
	_, err := FullBuild(ctx, memoryReader{sources: []SourceRecord{source}}, writer)
	if err != nil {
		t.Fatalf("FullBuild returned error: %v", err)
	}
	derived := writer.derived["TASK-001"]
	for _, kind := range []string{"heading", "task_status", "acceptance", "chunk"} {
		anchor := findAnchor(derived.CitationAnchors, kind)
		if anchor.ID == "" {
			t.Fatalf("missing %s anchor in %+v", kind, derived.CitationAnchors)
		}
		if anchor.SourceID != "TASK-001" || anchor.ContentHash != ContentHash(source.Body) || anchor.ByteEnd <= anchor.ByteStart || anchor.LineStart <= 0 || len(anchor.HeadingPath) == 0 {
			t.Fatalf("bad %s anchor: %+v", kind, anchor)
		}
	}
	if len(derived.AcceptanceRows) != 1 || derived.AcceptanceRows[0].AnchorID == "" {
		t.Fatalf("acceptance rows missing anchor: %+v", derived.AcceptanceRows)
	}
}

func TestParserLinkEdgeCases(t *testing.T) {
	ctx := context.Background()
	malformed := fixtureSource("DOC-1", "doc", "docs/bad.md", "Bad", "ready", "---\nkey without colon\n---\r\n# Bad\r\nSee [relative](other.md), [ambiguous](ALIAS-1), and [ok](DOC-2).\r\nInvalid: \xff\r\n")
	malformed.Aliases = []Alias{{Type: "id", ID: "ALIAS-LOCAL"}, {Type: "id", ID: "ALIAS-LOCAL"}}
	other := fixtureSource("DOC-2", "doc", "docs/ok.md", "Ok", "ready", "# Ok\n")
	ambiguousA := fixtureSource("DOC-3", "doc", "docs/a.md", "A", "ready", "# A\n")
	ambiguousA.Aliases = []Alias{{Type: "id", ID: "ALIAS-1"}}
	ambiguousB := fixtureSource("DOC-4", "doc", "docs/b.md", "B", "ready", "# B\n")
	ambiguousB.Aliases = []Alias{{Type: "id", ID: "ALIAS-1"}}
	dupeA := fixtureSource("DUP-1", "doc", "docs/dup-a.md", "Dup A", "ready", "# Dup A\n")
	dupeA.Aliases = []Alias{{Type: "id", ID: "DUPLICATE-1"}}
	dupeB := fixtureSource("DUP-2", "doc", "docs/dup-b.md", "Dup B", "ready", "# Dup B\n")
	dupeB.Aliases = []Alias{{Type: "id", ID: "DUPLICATE-1"}}
	writer := &memoryWriter{}
	report, err := FullBuild(ctx, memoryReader{sources: []SourceRecord{malformed, other, ambiguousA, ambiguousB, dupeA, dupeB}}, writer)
	if err != nil {
		t.Fatalf("FullBuild returned error: %v", err)
	}
	if !hasDiagnostic(report.Diagnostics, "DOC-1", "malformed_frontmatter") || !hasDiagnostic(report.Diagnostics, "DOC-1", "invalid_utf8") {
		t.Fatalf("missing parse diagnostics: %+v", report.Diagnostics)
	}
	if !hasDiagnostic(report.Diagnostics, "DUP-1", "duplicate_stable_id") || !hasDiagnostic(report.Diagnostics, "DUP-2", "duplicate_stable_id") {
		t.Fatalf("missing duplicate stable id diagnostics: %+v", report.Diagnostics)
	}
	if _, ok := writer.derived["DUP-1"]; ok {
		t.Fatalf("duplicate id source committed derived state")
	}
	broken := writer.derived["DOC-1"].BrokenLinks
	if !hasBroken(broken, "unresolved_relative_path") || !hasBroken(broken, "ambiguous_alias") {
		t.Fatalf("broken links = %+v, want relative path and ambiguous alias", broken)
	}
	if links := writer.derived["DOC-1"].Links; len(links) != 1 || links[0].TargetID != "DOC-2" {
		t.Fatalf("resolved links = %+v, want DOC-2 only", links)
	}
	parsed := ParseSource(malformed)
	heading := parsed.Headings[0]
	if heading.LineStart != 4 || heading.ByteStart != strings.Index(malformed.Body, "# Bad") {
		t.Fatalf("CRLF heading location = line %d byte %d, want line 4 byte %d", heading.LineStart, heading.ByteStart, strings.Index(malformed.Body, "# Bad"))
	}
	if len(aliasesFromMetadata(map[string]string{"aliases": "A, A"})) != 1 {
		t.Fatalf("same-source aliases were not deduplicated")
	}
}

func fixtureSource(id, kind, path, title, status, body string) SourceRecord {
	return SourceRecord{ID: id, Kind: kind, Path: path, Title: title, Status: status, Body: body, UpdatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)}
}

func withPreviousHash(source SourceRecord) SourceRecord {
	source.PreviousIndexedHash = ContentHash(source.Body)
	return source
}

type memoryReader struct{ sources []SourceRecord }

func (m memoryReader) ListSources(context.Context) ([]SourceRecord, error) {
	return append([]SourceRecord(nil), m.sources...), nil
}

type memoryWriter struct {
	derived      map[string]SourceDerived
	diagnostics  []CollisionDiagnostic
	replaceCalls int
}

func (m *memoryWriter) ReplaceSourceDerived(_ context.Context, derived SourceDerived) error {
	if m.derived == nil {
		m.derived = map[string]SourceDerived{}
	}
	m.derived[derived.SourceID] = derived
	m.replaceCalls++
	return nil
}

func (m *memoryWriter) WriteDiagnostics(_ context.Context, diagnostics []CollisionDiagnostic) error {
	m.diagnostics = append(m.diagnostics, diagnostics...)
	return nil
}

func (m *memoryWriter) ListDerivedLinks(context.Context) ([]DerivedLink, error) {
	var links []DerivedLink
	for _, derived := range m.derived {
		links = append(links, derived.Links...)
	}
	return links, nil
}

func (m *memoryWriter) ListBrokenLinks(context.Context) ([]BrokenLink, error) {
	var links []BrokenLink
	for _, derived := range m.derived {
		links = append(links, derived.BrokenLinks...)
	}
	return links, nil
}

func (m *memoryWriter) ListCitationAnchors(context.Context) ([]CitationAnchor, error) {
	var anchors []CitationAnchor
	for _, derived := range m.derived {
		anchors = append(anchors, derived.CitationAnchors...)
	}
	return anchors, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func findAnchor(anchors []CitationAnchor, kind string) CitationAnchor {
	for _, anchor := range anchors {
		if anchor.Kind == kind {
			return anchor
		}
	}
	return CitationAnchor{}
}

func hasDiagnostic(diagnostics []CollisionDiagnostic, sourceID, kind string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.SourceID == sourceID && diagnostic.Kind == kind {
			return true
		}
	}
	return false
}

func hasBroken(links []BrokenLink, reason string) bool {
	for _, link := range links {
		if link.Reason == reason {
			return true
		}
	}
	return false
}
