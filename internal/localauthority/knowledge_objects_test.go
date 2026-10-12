package localauthority

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

const knowledgeTestOperation authority.UUID = "12345678-1234-4234-8234-123456789abc"
const knowledgeTestEntity authority.UUID = "23456789-2345-4345-8345-23456789abcd"

func requireKnowledgeError(t *testing.T, err error, code Code) {
	t.Helper()
	var e Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatal("unexpected knowledge error classification")
	}
}

func TestKnowledgeObjectsExactCanonicalEncoding(t *testing.T) {
	markdown := []byte("# Exact\r\ncontent\nwithout normalization")
	c, err := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, knowledgeTestOperation, knowledgeTestEntity, markdown, false)
	if err != nil || c.byteLength != int64(len(markdown)) || c.contentHash != hash(markdown) || !bytes.Equal(c.blob.data, markdown) {
		t.Fatal("candidate normalized document content")
	}
	if c.blob.oid != hash(append([]byte(fmt.Sprintf("blob %d\x00", len(markdown))), markdown...)) || c.blob.oid == c.contentHash {
		t.Fatal("Git and content hashes conflated")
	}
	blobBytes, _ := hex.DecodeString(c.blob.oid)
	if !bytes.Equal(c.documents.data, append([]byte("100644 "+string(knowledgeTestEntity)+".md\x00"), blobBytes...)) {
		t.Fatal("document tree encoding changed")
	}
	entries, err := decodeDocumentTree(c.documents.data)
	if err != nil || entries[knowledgeTestEntity] != c.blob.oid || len(entries) != 1 {
		t.Fatal("exact document tree failed to round trip")
	}
	root, err := decodeKnowledgeRoot(c.root.data)
	if err != nil || root != c.documents.oid {
		t.Fatal("fixed root failed to round trip")
	}
	tree, parent, operation, err := decodeKnowledgeCommit(c.commit.data)
	if err != nil || tree != c.root.oid || parent != authority.EmptyKnowledgeRef || operation != knowledgeTestOperation {
		t.Fatal("exact commit failed to round trip")
	}
	want := "tree " + c.root.oid + "\nauthor Local Knowledge <knowledge@example.invalid> 0 +0000\ncommitter Local Knowledge <knowledge@example.invalid> 0 +0000\n\noperation " + string(knowledgeTestOperation) + "\n"
	if string(c.commit.data) != want {
		t.Fatal("synthetic commit identity or timestamp changed")
	}
	for i := 0; i < 3; i++ {
		again, e := planKnowledgeCandidate(nil, parent, operation, knowledgeTestEntity, markdown, false)
		if e != nil || !reflect.DeepEqual(c, again) {
			t.Fatal("same reserved intent changed candidate")
		}
	}
	markdown[0] = 'x'
	if c.blob.data[0] != '#' {
		t.Fatal("candidate aliases mutable caller bytes")
	}
	update, err := planKnowledgeCandidate(entries, c.commit.oid, operation, knowledgeTestEntity, []byte("new"), false)
	if err != nil || update.commit.oid == c.commit.oid || entries[knowledgeTestEntity] != c.blob.oid {
		t.Fatal("update lost reserved parent or mutated prior tree")
	}
	_, parent, _, err = decodeKnowledgeCommit(update.commit.data)
	if err != nil || parent != c.commit.oid {
		t.Fatal("update lacks exact parent")
	}
	tombstone, err := planKnowledgeCandidate(entries, c.commit.oid, operation, knowledgeTestEntity, nil, true)
	if err != nil || len(tombstone.documents.data) != 0 || tombstone.blob.oid != "" || tombstone.contentHash != "" || tombstone.byteLength != 0 {
		t.Fatal("tombstone fabricated a live locator")
	}
}

func TestKnowledgeTreeOrderingLimitsAndForeignFormat(t *testing.T) {
	other := authority.UUID("12345678-1234-4234-8234-123456789abc")
	entries := map[authority.UUID]string{knowledgeTestEntity: strings.Repeat("a", 64), other: strings.Repeat("b", 64)}
	tree, err := documentTree(entries)
	if err != nil || !strings.HasPrefix(string(tree.data), "100644 "+string(other)+".md\x00") {
		t.Fatal("tree is not byte sorted")
	}
	const record = len("100644 ") + 36 + len(".md") + 1 + 32
	for _, body := range [][]byte{
		append(append([]byte(nil), tree.data[record:]...), tree.data[:record]...),
		append(append([]byte(nil), tree.data...), tree.data[:record]...),
		tree.data[:len(tree.data)-1],
		bytes.Replace(tree.data, []byte("100644"), []byte("100755"), 1),
		bytes.Replace(tree.data, []byte(".md\x00"), []byte("/xx\x00"), 1),
		bytes.Replace(tree.data, bytes.Repeat([]byte{0xaa}, 32), bytes.Repeat([]byte{0}, 32), 1),
	} {
		_, err := decodeDocumentTree(body)
		requireKnowledgeError(t, err, KnowledgeCorrupt)
	}
	for _, input := range []map[authority.UUID]string{{"../../hooks": strings.Repeat("a", 64)}, {other: authority.EmptyKnowledgeRef}, {other: strings.Repeat("a", 40)}} {
		_, err := documentTree(input)
		requireKnowledgeError(t, err, KnowledgeCorrupt)
	}
	full := map[authority.UUID]string{}
	for i := 0; i < maxActiveDocuments+1; i++ {
		full[authority.UUID(fmt.Sprintf("%08x-1234-4234-8234-123456789abc", i))] = strings.Repeat("a", 64)
	}
	_, err = documentTree(full)
	requireKnowledgeError(t, err, Capacity)
	delete(full, "00001000-1234-4234-8234-123456789abc")
	fullTree, err := documentTree(full)
	if err != nil || len(fullTree.data) != maxKnowledgeTreeBytes {
		t.Fatal("exact tree byte/count capacity rejected")
	}
	if _, err := decodeDocumentTree(fullTree.data); err != nil {
		t.Fatal("bounded full tree rejected")
	}
	_, err = newKnowledgeObject("blob", make([]byte, maxKnowledgeBlobBytes+1))
	requireKnowledgeError(t, err, Capacity)
	_, err = newKnowledgeObject("tag", nil)
	requireKnowledgeError(t, err, KnowledgeCorrupt)
	c, _ := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, knowledgeTestOperation, knowledgeTestEntity, []byte("fixture"), false)
	for _, body := range [][]byte{nil, []byte("tree"), c.commit.data[:len(c.commit.data)-1], append(append([]byte(nil), c.commit.data...), '\n'), bytes.Replace(c.commit.data, []byte("0 +0000"), []byte("1 +0000"), 1), bytes.Replace(c.commit.data, []byte("operation "), []byte("prompt "), 1)} {
		_, _, _, err := decodeKnowledgeCommit(body)
		requireKnowledgeError(t, err, KnowledgeCorrupt)
	}
	for _, body := range [][]byte{nil, c.documents.data, append(append([]byte(nil), c.root.data...), 0), bytes.Replace(c.root.data, []byte("documents"), []byte("arbitrary"), 1)} {
		_, err := decodeKnowledgeRoot(body)
		requireKnowledgeError(t, err, KnowledgeCorrupt)
	}
}

// Native Git is an isolated offline format oracle ONLY. Production encoding
// never executes Git, inherits configuration or enables hooks/filters/helpers.
func TestKnowledgeObjectsNativeGitOracle(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("native Git format oracle is unavailable")
	}
	root := t.TempDir()
	emptyTemplate := filepath.Join(root, "empty-template")
	if os.Mkdir(emptyTemplate, 0700) != nil {
		t.Fatal("oracle template setup failed")
	}
	repo := filepath.Join(root, "oracle.git")
	run := func(input []byte, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Env = []string{"PATH=" + filepath.Dir(git), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal("isolated native Git format oracle failed")
		}
		return out
	}
	run(nil, "init", "--quiet", "--bare", "--object-format=sha256", "--template="+emptyTemplate, repo)
	c, err := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, knowledgeTestOperation, knowledgeTestEntity, []byte("# Offline fixture\r\n"), false)
	if err != nil {
		t.Fatal("oracle candidate failed")
	}
	for _, object := range []knowledgeObject{c.blob, c.documents, c.root, c.commit} {
		out := run(object.data, "--git-dir="+repo, "hash-object", "-w", "-t", object.kind, "--stdin")
		if string(out) != object.oid+"\n" {
			t.Fatal("owned format disagrees with native Git object identity")
		}
		out = run(nil, "--git-dir="+repo, "cat-file", object.kind, object.oid)
		if !bytes.Equal(out, object.data) {
			t.Fatal("native Git did not preserve exact object bytes")
		}
	}
	run(nil, "--git-dir="+repo, "update-ref", "refs/heads/knowledge", c.commit.oid)
	run(nil, "--git-dir="+repo, "fsck", "--strict", "--no-reflogs")
}
