package localauthority

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"gitcode-mcp/internal/authority"
)

const semanticTestBuild authority.UUID = "22222222-2222-4222-8222-222222222222"

func semanticFixtureNamespace(t *testing.T) semanticNamespace {
	t.Helper()
	m := testManifest()
	m.Network.DataEgress = "loopback_embeddings"
	n, err := newSemanticNamespace(m, semanticTestBuild, hash([]byte("fixture:tag")), hash([]byte("fixture-weights")), 2, semanticTestBuild)
	if err != nil {
		t.Fatal("namespace fixture invalid")
	}
	return n
}

func semanticSnapshot(t *testing.T, s *Store, kind authority.EntityKind) SnapshotPage {
	t.Helper()
	p, err := s.Snapshot(context.Background(), SnapshotRequest{ProjectID: testProject, Kind: kind, Limit: 100})
	if err != nil {
		t.Fatal("canonical snapshot failed")
	}
	return p
}

func semanticCapture(t *testing.T, p SnapshotPage) ([]semanticChunk, int) {
	t.Helper()
	if len(p.Entities) != 1 {
		t.Fatal("expected one canonical source")
	}
	c, skipped, err := captureSemanticChunks(context.Background(), p.Entities[0], p.Sequence)
	if err != nil {
		t.Fatal("canonical chunk capture failed")
	}
	return c, skipped
}

func semanticFixtureShard(t *testing.T) (semanticNamespace, semanticShard, uint64) {
	t.Helper()
	s, _, _ := testStore(t)
	createIssue(t, s, "semantic-issue")
	p := semanticSnapshot(t, s, authority.Issue)
	chunks, _ := semanticCapture(t, p)
	return semanticFixtureNamespace(t), semanticShard{[]semanticRecord{chunks[0].record}, [][]float32{{0, 1}}}, p.Sequence
}

func semanticFixtureGeneration(n semanticNamespace, build authority.UUID, seq uint64, shards ...[]byte) semanticGeneration {
	g := semanticGeneration{Schema: 1, Namespace: n, Epoch: semanticTestBuild, BuildID: build, Generation: 1, SelectionSequence: seq, RevalidatedSequence: seq, Selection: semanticSelection{Sources: 1, Exhausted: true, State: "complete", Reason: "none"}, Shards: []semanticShardDescriptor{}}
	for i, b := range shards {
		rows := int(binary.LittleEndian.Uint16(b[14:16]))
		elems := rows * n.Dimensions
		g.Shards = append(g.Shards, semanticShardDescriptor{i, hash(b), len(b), rows, elems})
		g.ShardBytes += len(b)
		g.Elements += elems
		g.Selection.Stored += rows
	}
	g.Selection.Eligible = g.Selection.Stored
	return g
}

func TestSemanticNamespacePureBinding(t *testing.T) {
	n := semanticFixtureNamespace(t)
	if !n.valid() || !validDigest(n.digest()) {
		t.Fatal("invalid binding")
	}
	for _, change := range []func(*semanticNamespace){func(x *semanticNamespace) { x.ModelTagHash = hash([]byte("different:tag")) }, func(x *semanticNamespace) { x.ModelDigest = hash([]byte("different weights")) }, func(x *semanticNamespace) { x.ProvisioningRevision = testProject }, func(x *semanticNamespace) { x.TrackerInstance = testProject }, func(x *semanticNamespace) { x.Dimensions = 3 }} {
		x := n
		change(&x)
		if !x.valid() || x.digest() == n.digest() {
			t.Fatal("namespace compatibility falsely retained")
		}
	}
	if _, err := newSemanticNamespace(testManifest(), semanticTestBuild, n.ModelTagHash, n.ModelDigest, 2, semanticTestBuild); err == nil {
		t.Fatal("deny policy admitted semantic binding")
	}
	if reflect.TypeOf(semanticChunk{}).NumField() != 2 {
		t.Fatal("transient carrier changed")
	}
	b, _ := json.Marshal(semanticChunk{input: "private-content-sentinel"})
	if bytes.Contains(b, []byte("private-content-sentinel")) {
		t.Fatal("transient bytes serialized")
	}
}

func TestSemanticCanonicalPayloadEditAndRevisionReuse(t *testing.T) {
	s, _, _ := testStore(t)
	first := createIssue(t, s, "semantic-original")
	p := semanticSnapshot(t, s, authority.Issue)
	original, _ := semanticCapture(t, p)
	second, err := s.Mutate(context.Background(), request(t, "issue.update", "semantic-same-payload", authority.IssuePayload{Title: "Synthetic task", Body: "synthetic evidence", State: "open"}, first), testActor)
	if err != nil {
		t.Fatal("new revision failed")
	}
	p = semanticSnapshot(t, s, authority.Issue)
	same, _ := semanticCapture(t, p)
	a, b := original[0].record, same[0].record
	if a.Citation == b.Citation || a.SourceSequence >= b.SourceSequence || a.ContentDigest != b.ContentDigest || a.TextDigest != b.TextDigest || a.ChunkDigest != b.ChunkDigest {
		t.Fatal("unchanged-content/new-revision proof lost")
	}
	_, err = s.Mutate(context.Background(), request(t, "issue.update", "semantic-edited", authority.IssuePayload{Title: "Synthetic task", Body: "changed canonical text", State: "open"}, second), testActor)
	if err != nil {
		t.Fatal("content update failed")
	}
	p = semanticSnapshot(t, s, authority.Issue)
	edited, _ := semanticCapture(t, p)
	if edited[0].record.ContentDigest == b.ContentDigest || edited[0].record.TextDigest == b.TextDigest || edited[0].record.ChunkDigest == b.ChunkDigest {
		t.Fatal("edit reused stale proof")
	}
	invalid := p.Entities[0]
	invalid.ContentDigest = original[0].record.ContentDigest
	if _, _, err = captureSemanticChunks(context.Background(), invalid, p.Sequence); err == nil {
		t.Fatal("substituted snapshot digest accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = captureSemanticChunks(ctx, p.Entities[0], p.Sequence); err != context.Canceled {
		t.Fatal("cancelled capture proceeded")
	}
}

func TestSemanticCanonicalArchiveAndGitDocumentTombstone(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	label, err := s.Mutate(context.Background(), request(t, "label.create", "semantic-label", authority.LabelPayload{Name: "synthetic-label", Color: "#112233"}), testActor)
	if err != nil {
		t.Fatal("label create failed")
	}
	c, _ := semanticCapture(t, semanticSnapshot(t, s, authority.Label))
	if len(c) != 1 {
		t.Fatal("active label not captured")
	}
	_, err = s.Mutate(context.Background(), request(t, "label.update", "semantic-label-archive", authority.LabelPayload{Name: "synthetic-label", Color: "#112233", Archived: true}, label), testActor)
	if err != nil {
		t.Fatal("archive failed")
	}
	c, _ = semanticCapture(t, semanticSnapshot(t, s, authority.Label))
	if len(c) != 0 {
		t.Fatal("archived label eligible")
	}
	doc, err := s.MutateDocument(context.Background(), documentRequest(t, "document.create", "semantic-git-document", authority.EmptyKnowledgeRef, authority.Citation{}, "# Git-only-document-sentinel\n"), testActor)
	if err != nil {
		t.Fatal("Git document create failed")
	}
	p := semanticSnapshot(t, s, authority.Document)
	c, _ = semanticCapture(t, p)
	if len(c) != 1 || !strings.Contains(c[0].input, "Git-only-document-sentinel") {
		t.Fatal("actual Git hydration missing")
	}
	n := semanticFixtureNamespace(t)
	c[0].record.DocumentLocator = doc.Result.Locator
	b, err := encodeSemanticShard(semanticShard{[]semanticRecord{c[0].record}, [][]float32{{1, 0}}}, n, semanticTestBuild, 0, p.Sequence)
	if err != nil || bytes.Contains(b, []byte("Git-only-document-sentinel")) || bytes.Contains(b, []byte("Example document")) {
		t.Fatal("source text persisted in vector format")
	}
	if _, err = decodeSemanticShard(b, n, semanticTestBuild, 0, p.Sequence); err != nil {
		t.Fatal("actual locator rejected")
	}
	_, err = s.MutateDocument(context.Background(), documentRequest(t, "document.tombstone", "semantic-git-tombstone", doc.Result.Locator.CommitOID, doc.Receipt.Citation, ""), testActor)
	if err != nil {
		t.Fatal("Git tombstone failed")
	}
	c, _ = semanticCapture(t, semanticSnapshot(t, s, authority.Document))
	if len(c) != 0 {
		t.Fatal("tombstone eligible")
	}
}

func TestSemanticUTF8SpansWhitespaceAndMaximumDocument(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	// The fixed title/newline prefix creates an eligible first chunk; later
	// whitespace spans are not invented embeddings. Exact boundary is tested.
	for i, markdown := range []string{strings.Repeat(" ", 8192) + "end", strings.Repeat("界", (256<<10)/3), strings.Repeat("x", 4096-len("Example document\n"))} {
		d, err := s.MutateDocument(context.Background(), documentRequest(t, "document.create", []string{"semantic-spaces", "semantic-unicode", "semantic-exact-boundary"}[i], func() string {
			st, e := s.KnowledgeStatus(context.Background(), testProject)
			if e != nil {
				t.Fatal("status failed")
			}
			return st.CurrentRefOID
		}(), authority.Citation{}, markdown), testActor)
		if err != nil {
			t.Fatal("bounded document create failed")
		}
		p := semanticSnapshot(t, s, authority.Document)
		var entry SnapshotEntity
		for _, e := range p.Entities {
			if e.Entity.Citation == d.Receipt.Citation {
				entry = e
			}
		}
		c, skipped, err := captureSemanticChunks(context.Background(), entry, p.Sequence)
		if err != nil || len(c)+skipped > semanticSourceChunks {
			t.Fatal("bounded chunk capture failed")
		}
		if i == 0 && skipped == 0 {
			t.Fatal("whitespace chunks embedded")
		}
		for j, x := range c {
			if !utf8.ValidString(x.input) || len(x.input) > 4096 || hash([]byte(x.input)) != x.record.ChunkDigest || j > 0 && !semanticOrdered(c[j-1].record, x.record) {
				t.Fatal("UTF8/span proof failed")
			}
		}
		if i == 2 && (len(c) != 1 || c[0].record.End != 4096) {
			t.Fatal("exact chunk boundary failed")
		}
	}
}

func TestSemanticShardExactHeaderRoundTripAndRefusal(t *testing.T) {
	n, s, seq := semanticFixtureShard(t)
	s.vectors[0][0] = math.Float32frombits(0x80000000)
	b, err := encodeSemanticShard(s, n, semanticTestBuild, 0, seq)
	if err != nil {
		t.Fatal("zero component rejected")
	}
	d, err := decodeSemanticShard(b, n, semanticTestBuild, 0, seq)
	if err != nil || !reflect.DeepEqual(d.records, s.records) || math.Float32bits(d.vectors[0][0]) != 0 {
		t.Fatal("canonical roundtrip failed")
	}
	if string(b[:8]) != "GCSVEC01" || binary.LittleEndian.Uint16(b[8:10]) != 1 || b[11] != 0 || binary.LittleEndian.Uint16(b[12:14]) != 2 || binary.LittleEndian.Uint16(b[14:16]) != 1 || binary.LittleEndian.Uint32(b[20:24]) != 2 {
		t.Fatal("header contract drift")
	}
	for _, change := range []func([]byte) []byte{
		func(x []byte) []byte { x[0] ^= 1; return x }, func(x []byte) []byte { x[8] = 2; return x }, func(x []byte) []byte { x[10] = 1; return x }, func(x []byte) []byte { x[11] = 1; return x }, func(x []byte) []byte { x[12] = 3; return x }, func(x []byte) []byte { x[14] = 0; return x }, func(x []byte) []byte { x[24] ^= 1; return x }, func(x []byte) []byte { x[56] ^= 1; return x },
		func(x []byte) []byte { binary.LittleEndian.PutUint32(x[16:20], math.MaxUint32); return x }, func(x []byte) []byte { binary.LittleEndian.PutUint32(x[20:24], math.MaxUint32); return x },
		func(x []byte) []byte { return x[:len(x)-1] }, func(x []byte) []byte { return append(x, 0) },
		func(x []byte) []byte { binary.LittleEndian.PutUint32(x[len(x)-8:], 0x80000000); return x }, func(x []byte) []byte {
			binary.LittleEndian.PutUint32(x[len(x)-4:], math.Float32bits(float32(math.NaN())))
			return x
		}, func(x []byte) []byte {
			binary.LittleEndian.PutUint32(x[len(x)-4:], math.Float32bits(float32(math.Inf(1))))
			return x
		}, func(x []byte) []byte { binary.LittleEndian.PutUint32(x[len(x)-4:], 0); return x }, func(x []byte) []byte { binary.LittleEndian.PutUint32(x[len(x)-4:], math.Float32bits(0.5)); return x },
	} {
		x := change(append([]byte{}, b...))
		if _, err = decodeSemanticShard(x, n, semanticTestBuild, 0, seq); err == nil {
			t.Fatal("malformed shard accepted")
		}
	}
	if _, err = decodeSemanticShard(b, n, testProject, 0, seq); err == nil {
		t.Fatal("foreign build accepted")
	}
	other := n
	other.ProvisioningRevision = testProject
	if _, err = decodeSemanticShard(b, other, semanticTestBuild, 0, seq); err == nil {
		t.Fatal("foreign namespace accepted")
	}
}

func TestSemanticClosedMetadataAndCapacity(t *testing.T) {
	n, s, seq := semanticFixtureShard(t)
	base, err := encodeSemanticShard(s, n, semanticTestBuild, 0, seq)
	if err != nil {
		t.Fatal("encode failed")
	}
	metaSize := int(binary.LittleEndian.Uint32(base[16:20]))
	meta := base[72 : 72+metaSize]
	for _, invalid := range [][]byte{
		bytes.Replace(meta, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1),
		bytes.Replace(meta, []byte(`"schema":1`), []byte(`"schema":1,"unknown":true`), 1),
		bytes.Replace(meta, []byte(`"ordinal":0`), []byte(`"ordinal":null`), 1),
		bytes.Replace(meta, []byte(`"ordinal":0`), []byte(`"ordinal":1`), 1),
		bytes.Replace(meta, []byte(`"start":0`), []byte(`"start":1`), 1),
		bytes.Replace(meta, []byte(`"ordinal":0`), []byte(`"ordinal":0,"document_locator":null`), 1),
		bytes.Replace(meta, []byte(`"ordinal":0,`), nil, 1), append(append([]byte{}, meta...), []byte(` {}`)...),
	} {
		x := append([]byte{}, base[:72]...)
		binary.LittleEndian.PutUint32(x[16:20], uint32(len(invalid)))
		x = append(x, invalid...)
		x = append(x, base[72+metaSize:]...)
		if _, err = decodeSemanticShard(x, n, semanticTestBuild, 0, seq); err == nil {
			t.Fatal("open/null/missing metadata accepted")
		}
	}
	for _, dims := range []int{1, 2, 1024, 4096} {
		x := n
		x.Dimensions = dims
		rows := semanticShardRows
		if rows*dims > semanticShardElements {
			rows = semanticShardElements / dims
		}
		candidate := semanticShard{records: make([]semanticRecord, rows), vectors: make([][]float32, rows)}
		for i := 0; i < rows; i++ {
			r := s.records[0]
			r.Citation.EntityID = authority.UUID(strings.Replace(string(semanticTestBuild), "222222222222", fmtSemanticSuffix(i+1), 1))
			candidate.records[i] = r
			v := make([]float32, dims)
			v[0] = 1
			candidate.vectors[i] = v
		}
		b, err := encodeSemanticShard(candidate, x, semanticTestBuild, 0, seq)
		if err != nil {
			t.Fatal("capacity boundary rejected")
		}
		if _, err = decodeSemanticShard(b, x, semanticTestBuild, 0, seq); err != nil {
			t.Fatal("capacity roundtrip failed")
		}
		candidate.records = append(candidate.records, candidate.records[len(candidate.records)-1])
		candidate.vectors = append(candidate.vectors, candidate.vectors[0])
		if _, err = encodeSemanticShard(candidate, x, semanticTestBuild, 0, seq); err == nil {
			t.Fatal("capacity/duplicate overflow accepted")
		}
	}
}

func TestSemanticActualEmptyAndInactiveGeneration(t *testing.T) {
	s, _, _ := testStore(t)
	n := semanticFixtureNamespace(t)
	p := semanticSnapshot(t, s, "")
	if len(p.Entities) != 0 || p.Sequence != 0 || p.NextCursor != "" {
		t.Fatal("empty canonical snapshot changed")
	}
	g := semanticFixtureGeneration(n, semanticTestBuild, p.Sequence)
	g.Selection.Sources = p.Scanned
	b, err := encodeSemanticGeneration(g)
	if err != nil {
		t.Fatal("actual empty manifest failed")
	}
	if _, err = decodeSemanticGeneration(b, n, semanticTestBuild, nil); err != nil {
		t.Fatal("actual empty roundtrip failed")
	}
	_, err = s.Mutate(context.Background(), request(t, "label.create", "semantic-inactive-source", authority.LabelPayload{Name: "inactive", Color: "#112233", Archived: true}), testActor)
	if err != nil {
		t.Fatal("inactive source create failed")
	}
	p = semanticSnapshot(t, s, "")
	chunks, skipped := semanticCapture(t, p)
	if len(chunks) != 0 || skipped != 0 {
		t.Fatal("inactive source invented chunks")
	}
	g = semanticFixtureGeneration(n, semanticTestBuild, p.Sequence)
	g.Selection.Sources = p.Scanned
	b, err = encodeSemanticGeneration(g)
	if err != nil {
		t.Fatal("nonempty canonical/zero-vector manifest failed")
	}
	if _, err = decodeSemanticGeneration(b, n, semanticTestBuild, nil); err != nil {
		t.Fatal("inactive generation roundtrip failed")
	}
}

func TestSemanticDistinctSourcesAcrossShardBoundary(t *testing.T) {
	n, s, seq := semanticFixtureShard(t)
	s.records[0].Citation.EntityID = "22222222-2222-4222-8222-222222222221"
	a, err := encodeSemanticShard(s, n, semanticTestBuild, 0, seq)
	if err != nil {
		t.Fatal("first shard fixture failed")
	}
	s.records[0].Citation.EntityID = semanticTestBuild
	b, err := encodeSemanticShard(s, n, semanticTestBuild, 1, seq)
	if err != nil {
		t.Fatal("second shard fixture failed")
	}
	g := semanticFixtureGeneration(n, semanticTestBuild, seq, a, b)
	m, err := encodeSemanticGeneration(g)
	if err != nil {
		t.Fatal("negative counter fixture failed")
	}
	if _, err = decodeSemanticGeneration(m, n, semanticTestBuild, [][]byte{a, b}); err == nil {
		t.Fatal("cross-shard distinct source undercount accepted")
	}
	// Sources with no eligible rows may make the declared count larger, but
	// never smaller. Sequence is independently bounded even in this codec.
	g.Selection.Sources = 2
	g.SelectionSequence = 2
	g.RevalidatedSequence = 2
	m, err = encodeSemanticGeneration(g)
	if err != nil {
		t.Fatal("conserved two-source fixture failed")
	}
	if _, err = decodeSemanticGeneration(m, n, semanticTestBuild, [][]byte{a, b}); err != nil {
		t.Fatal("conserved cross-shard rows rejected")
	}
}

func fmtSemanticSuffix(i int) string {
	const digits = "0123456789abcdef"
	b := []byte("000000000000")
	for j := len(b) - 1; i > 0; j-- {
		b[j] = digits[i%16]
		i /= 16
	}
	return string(b)
}

func TestSemanticGenerationEmptyPartialHeadAndCrossShardConservation(t *testing.T) {
	n, s, seq := semanticFixtureShard(t)
	a, err := encodeSemanticShard(s, n, semanticTestBuild, 0, seq)
	if err != nil {
		t.Fatal("first shard failed")
	}
	g := semanticFixtureGeneration(n, semanticTestBuild, seq, a)
	manifest, err := encodeSemanticGeneration(g)
	if err != nil {
		t.Fatal("manifest encode failed")
	}
	if _, err = decodeSemanticGeneration(manifest, n, semanticTestBuild, [][]byte{a}); err != nil {
		t.Fatal("generation roundtrip failed")
	}
	h := semanticHead{1, n.digest(), g.Epoch, g.BuildID, g.Generation, hash(manifest)}
	head, err := encodeSemanticHead(h)
	if err != nil {
		t.Fatal("head encode failed")
	}
	if _, err = decodeSemanticHead(head, g, manifest); err != nil {
		t.Fatal("head binding failed")
	}
	wrong := g
	wrong.Generation++
	if _, err = decodeSemanticHead(head, wrong, manifest); err == nil {
		t.Fatal("head generation substitution accepted")
	}
	second, err := encodeSemanticShard(s, n, semanticTestBuild, 1, seq)
	if err != nil {
		t.Fatal("second fixture shard failed")
	}
	duplicate := semanticFixtureGeneration(n, semanticTestBuild, seq, a, second)
	m, err := encodeSemanticGeneration(duplicate)
	if err != nil {
		t.Fatal("descriptor fixture invalid")
	}
	if _, err = decodeSemanticGeneration(m, n, semanticTestBuild, [][]byte{a, second}); err == nil {
		t.Fatal("cross-shard duplicate accepted")
	}
	for _, change := range []func(*semanticGeneration){func(x *semanticGeneration) { x.ShardBytes++ }, func(x *semanticGeneration) { x.Elements++ }, func(x *semanticGeneration) { x.Selection.Stored++ }, func(x *semanticGeneration) { x.SelectionSequence = 0 }, func(x *semanticGeneration) { x.RevalidatedSequence = 0 }, func(x *semanticGeneration) { x.Shards[0].Ordinal = 1 }} {
		x := g
		x.Shards = append([]semanticShardDescriptor{}, g.Shards...)
		change(&x)
		if _, err = encodeSemanticGeneration(x); err == nil {
			t.Fatal("conservation/sequence violation accepted")
		}
	}
	empty := semanticFixtureGeneration(n, semanticTestBuild, 0)
	empty.Selection.Sources = 0
	m, err = encodeSemanticGeneration(empty)
	if err != nil {
		t.Fatal("empty generation invented a requirement")
	}
	if _, err = decodeSemanticGeneration(m, n, semanticTestBuild, nil); err != nil {
		t.Fatal("empty generation roundtrip failed")
	}
	if _, err = encodeSemanticShard(semanticShard{}, n, semanticTestBuild, 0, 0); err == nil {
		t.Fatal("empty shard accepted")
	}
	partial := g
	partial.Selection.Exhausted = false
	partial.Selection.State = "bounded_partial"
	partial.Selection.Reason = "inspection_turn_limit"
	partial.Selection.Eligible++
	partial.Selection.Omitted++
	m, err = encodeSemanticGeneration(partial)
	if err != nil {
		t.Fatal("honest partial rejected")
	}
	if _, err = decodeSemanticGeneration(m, n, semanticTestBuild, [][]byte{a}); err != nil {
		t.Fatal("partial roundtrip failed")
	}
	partial.Selection.State = "complete"
	partial.Selection.Reason = "none"
	if _, err = encodeSemanticGeneration(partial); err == nil {
		t.Fatal("bounded selection claimed complete")
	}
}
