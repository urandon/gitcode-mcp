package localgit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func fixtureGit(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z"}
	cmd.Stdin = strings.NewReader(input)
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("fixture Git %s failed", args[0])
	}
	return strings.TrimSpace(string(data))
}

func newFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data.git")
	fixtureGit(t, dir, "", "init", "--bare", dir)
	fixtureGit(t, dir, "", "symbolic-ref", "HEAD", "refs/heads/main")
	return dir
}

func fixtureCommit(t *testing.T, dir string, files map[string]string, parent string) string {
	t.Helper()
	var tree func(map[string]string) string
	tree = func(files map[string]string) string {
		groups := map[string]map[string]string{}
		records := map[string]string{}
		for path, content := range files {
			first, rest, nested := strings.Cut(path, "/")
			if nested {
				if groups[first] == nil {
					groups[first] = map[string]string{}
				}
				groups[first][rest] = content
			} else {
				oid := fixtureGit(t, dir, content, "hash-object", "-w", "--stdin")
				records[first] = "100644 blob " + oid + "\t" + first + "\n"
			}
		}
		for name, group := range groups {
			records[name] = "040000 tree " + tree(group) + "\t" + name + "\n"
		}
		names := make([]string, 0, len(records))
		for n := range records {
			names = append(names, n)
		}
		sort.Strings(names)
		var body strings.Builder
		for _, n := range names {
			body.WriteString(records[n])
		}
		return fixtureGit(t, dir, body.String(), "mktree")
	}
	args := []string{"commit-tree", tree(files)}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	commit := fixtureGit(t, dir, "Fixture project\n", args...)
	fixtureGit(t, dir, "", "update-ref", "refs/heads/main", commit)
	return commit
}

func fixtureFiles() map[string]string {
	return map[string]string{
		".harness/project.yaml":                "schema: 1\nproject_id: fixture-project\nnext_issue: 3\nnext_comment: 2\nnext_document: 2\n",
		"issues/ISSUE-1/issue.md":              "---\nschema: 1\nid: ISSUE-1\nkind: issue\ntitle: First issue\nstate: open\nlabels: [research]\nmilestone: Phase one\nlinks: [{kind: wiki, id: DOC-1}]\n---\nIssue body.\n",
		"issues/ISSUE-2/issue.md":              "---\nschema: 1\nid: ISSUE-2\nkind: issue\ntitle: Retired issue\nstate: deleted\n---\nTombstone.\n",
		"issues/ISSUE-1/comments/COMMENT-1.md": "---\nschema: 1\nid: COMMENT-1\nkind: comment\ntitle: Evidence\nstate: active\nparent_id: ISSUE-1\n---\nComment body.\n",
		"wiki/DOC-1.md":                        "---\nschema: 1\nid: DOC-1\nkind: wiki\ntitle: Design note\nstate: active\n---\nWiki body.\n",
	}
}

func openFixture(t *testing.T, dir string, limits Limits) *Store {
	t.Helper()
	s, err := Open(context.Background(), dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func pinFixture(t *testing.T, s *Store) *Snapshot {
	t.Helper()
	snap, err := s.Snapshot(context.Background(), "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected %s; got %v", code, err)
	}
}

func TestSnapshotBareProjectGetListAndReceipt(t *testing.T) {
	dir := newFixture(t)
	files := fixtureFiles()
	key := strings.Repeat("a", 64)
	blob := fixtureGit(t, dir, files["issues/ISSUE-1/issue.md"], "hash-object", "-w", "--stdin")
	r := Receipt{Schema: 1, ProjectID: "fixture-project", KeyHash: key, IntentHash: strings.Repeat("b", 64), Operation: "create_issue", Result: Reference{Issue, "ISSUE-1"}, ResultBlob: blob}
	raw, _ := json.Marshal(r)
	files[".harness/receipts/"+key+".json"] = string(raw)
	commit := fixtureCommit(t, dir, files, "")
	snap := pinFixture(t, openFixture(t, dir, Limits{}))
	for _, ref := range []Reference{{Issue, "ISSUE-1"}, {Comment, "COMMENT-1"}, {Wiki, "DOC-1"}} {
		e, err := snap.Get(context.Background(), ref.Kind, ref.ID)
		if err != nil {
			t.Fatal(err)
		}
		if e.Citation.ProjectID != "fixture-project" || e.Citation.Commit != commit || e.Metadata.ID != ref.ID || e.Body == "" {
			t.Fatal("incomplete entity/citation")
		}
	}
	issues, err := snap.List(context.Background(), Issue, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].Metadata.ID != "ISSUE-1" || issues[1].Metadata.State != "deleted" {
		t.Fatal("list order/tombstone mismatch")
	}
	got, cite, err := snap.Receipt(context.Background(), key)
	if err != nil || got != r || cite.Commit != commit {
		t.Fatal("receipt read mismatch")
	}
	_, err = snap.Get(context.Background(), Issue, "ISSUE-99")
	requireCode(t, err, "not_found")
	_, err = snap.Get(context.Background(), Issue, "ISSUE-01")
	requireCode(t, err, "invalid_argument")
	_, err = snap.List(context.Background(), Issue, 0, 129)
	requireCode(t, err, "invalid_argument")
}

func TestSnapshotPinnedWhenHeadMovesAndBoundedHistory(t *testing.T) {
	dir := newFixture(t)
	files := fixtureFiles()
	first := fixtureCommit(t, dir, files, "")
	s := openFixture(t, dir, Limits{})
	old := pinFixture(t, s)
	files["issues/ISSUE-1/issue.md"] = strings.ReplaceAll(files["issues/ISSUE-1/issue.md"], "Issue body.", "Changed body.")
	second := fixtureCommit(t, dir, files, first)
	current := pinFixture(t, s)
	e, err := old.Get(context.Background(), Issue, "ISSUE-1")
	if err != nil || e.Body != "Issue body.\n" || e.Citation.Commit != first {
		t.Fatal("old snapshot changed")
	}
	history, err := current.History(context.Background(), Issue, "ISSUE-1", 2)
	if err != nil || len(history) != 2 || history[0].Commit != second || history[1].Commit != first {
		t.Fatalf("history: %v", err)
	}
	_, err = current.History(context.Background(), Issue, "ISSUE-1", 1)
	requireCode(t, err, "history_unavailable")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.Snapshot(ctx, "refs/heads/main")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
}

func TestSchemaRejectsMalformedUnknownOversizeAndMissingFiles(t *testing.T) {
	tests := []struct {
		name, path, content, code string
		snapshot                  bool
	}{
		{"unknown-project", ".harness/project.yaml", "schema: 9\nproject_id: fixture-project\n", "unsupported_schema", true},
		{"missing-project", ".harness/project.yaml", "", "not_found", true},
		{"counter-reuse", ".harness/project.yaml", "schema: 1\nproject_id: fixture-project\nnext_issue: 1\nnext_comment: 2\nnext_document: 2\n", "invalid_schema", true},
		{"malformed", "issues/ISSUE-1/issue.md", "---\ninvalid: [\n---\nprivate fixture sentinel\n", "invalid_schema", false},
		{"unknown-entity", "issues/ISSUE-1/issue.md", "---\nschema: 9\n---\n", "unsupported_schema", false},
		{"unknown-field", "issues/ISSUE-1/issue.md", "---\nschema: 1\nid: ISSUE-1\nkind: issue\ntitle: First\nstate: open\nunknown: sentinel\n---\n", "invalid_schema", false},
		{"wrong-id", "issues/ISSUE-1/issue.md", "---\nschema: 1\nid: ISSUE-2\nkind: issue\ntitle: First\nstate: open\n---\n", "invalid_schema", false},
		{"duplicate-key", "issues/ISSUE-1/issue.md", "---\nschema: 1\nschema: 1\n---\n", "invalid_schema", false},
		{"anchor", "issues/ISSUE-1/issue.md", "---\nschema: 1\ntitle: &x sentinel\nid: ISSUE-1\nkind: issue\nstate: open\n---\n", "invalid_schema", false},
		{"too-big", "issues/ISSUE-1/issue.md", strings.Repeat("x", MaxFileBytes+1), "limit_exceeded", false},
		{"nul", "issues/ISSUE-1/issue.md", "---\nschema: 1\n---\n\x00", "invalid_schema", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := newFixture(t)
			files := fixtureFiles()
			if tc.content == "" {
				delete(files, tc.path)
			} else {
				files[tc.path] = tc.content
			}
			fixtureCommit(t, dir, files, "")
			s := openFixture(t, dir, Limits{})
			snap, err := s.Snapshot(context.Background(), "HEAD")
			if !tc.snapshot && err == nil {
				_, err = snap.Get(context.Background(), Issue, "ISSUE-1")
			}
			requireCode(t, err, tc.code)
			if strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), dir) {
				t.Fatal("input/path leaked into error")
			}
		})
	}
}

func TestLimitsAndRestrictedGitEnvironment(t *testing.T) {
	dir := newFixture(t)
	files := fixtureFiles()
	commit := fixtureCommit(t, dir, files, "")
	foreign := newFixture(t)
	foreignFiles := fixtureFiles()
	foreignFiles[".harness/project.yaml"] = strings.ReplaceAll(foreignFiles[".harness/project.yaml"], "fixture-project", "foreign-project")
	fixtureCommit(t, foreign, foreignFiles, "")
	for k, v := range map[string]string{"GIT_DIR": foreign, "GIT_COMMON_DIR": foreign, "GIT_OBJECT_DIRECTORY": filepath.Join(foreign, "objects"), "GIT_NAMESPACE": "foreign", "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.bare", "GIT_CONFIG_VALUE_0": "false", "GIT_TRACE": filepath.Join(t.TempDir(), "trace")} {
		t.Setenv(k, v)
	}
	snap := pinFixture(t, openFixture(t, dir, Limits{}))
	if snap.Commit() != commit || snap.Project().ID != "fixture-project" {
		t.Fatal("inherited Git environment redirected store")
	}
	_, err := Open(context.Background(), dir, Limits{Entries: MaxEntries + 1})
	requireCode(t, err, "invalid_argument")
	_, err = openFixture(t, dir, Limits{TreeBytes: 10}).Snapshot(context.Background(), "HEAD")
	requireCode(t, err, "limit_exceeded")
	_, err = openFixture(t, dir, Limits{Entries: 1}).Snapshot(context.Background(), "HEAD")
	requireCode(t, err, "limit_exceeded")
	_, err = pinFixture(t, openFixture(t, dir, Limits{ReadBytes: 1})).List(context.Background(), Issue, 0, 100)
	requireCode(t, err, "limit_exceeded")
	_, err = pinFixture(t, openFixture(t, dir, Limits{FileBytes: 100})).Get(context.Background(), Issue, "ISSUE-1")
	requireCode(t, err, "limit_exceeded")
	_, err = pinFixture(t, openFixture(t, dir, Limits{MetadataBytes: 100})).Get(context.Background(), Issue, "ISSUE-1")
	requireCode(t, err, "limit_exceeded")
	if err := os.WriteFile(filepath.Join(dir, "objects/info/alternates"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal("fixture write failed")
	}
	_, err = Open(context.Background(), dir, Limits{})
	requireCode(t, err, "unsupported_store")
}

func TestContentFreeReceiptAndMetadataValidation(t *testing.T) {
	key := strings.Repeat("a", 64)
	r := Receipt{Schema: 1, ProjectID: "fixture-project", KeyHash: key, IntentHash: strings.Repeat("b", 64), Operation: "create_issue", Result: Reference{Issue, "ISSUE-1"}, ResultBlob: strings.Repeat("c", 40)}
	data, _ := json.Marshal(r)
	_, err := decodeReceipt([]byte(strings.TrimSuffix(string(data), "}")+`,"body":"private sentinel"}`), "fixture-project", key)
	requireCode(t, err, "invalid_schema")
	_, err = decodeReceipt(data, "different-project", key)
	requireCode(t, err, "invalid_schema")
	_, err = decodeReceipt([]byte(strings.TrimSuffix(string(data), "}")+`,"schema":1}`), "fixture-project", key)
	requireCode(t, err, "invalid_schema")
	_, err = decodeReceipt([]byte(strings.Replace(string(data), `"schema":1`, `"schema":9,"Schema":1`, 1)), "fixture-project", key)
	requireCode(t, err, "invalid_schema")
	_, err = decodeReceipt([]byte(strings.Replace(string(data), `"kind":"issue"`, `"kind":"wiki","Kind":"issue"`, 1)), "fixture-project", key)
	requireCode(t, err, "invalid_schema")
	_, err = decodeReceipt([]byte(strings.Replace(string(data), `"project_id"`, `"Project_ID"`, 1)), "fixture-project", key)
	requireCode(t, err, "invalid_schema")
	for _, m := range []Metadata{{Schema: 1, ID: "ISSUE-1", Kind: Issue, Title: "x", State: "active"}, {Schema: 1, ID: "COMMENT-1", Kind: Comment, Title: "x", State: "active"}, {Schema: 1, ID: "DOC-1", Kind: Wiki, Title: "x", State: "active", Labels: []string{"x"}}} {
		requireCode(t, validateMetadata(m), "invalid_schema")
	}
	for _, id := range []string{"ISSUE-0", "ISSUE-01", "ISSUE--1", "ISSUE-1/../../x", fmt.Sprint("ISSUE-", maxID+1)} {
		_, err := idNumber(Issue, id)
		requireCode(t, err, "invalid_argument")
	}
}

func TestYAMLMergeIsNotPartOfProjectSchema(t *testing.T) {
	data := []byte("---\nschema: 1\nid: ISSUE-1\nkind: issue\nstate: open\n<<: {title: injected}\n---\nBody.\n")
	_, err := decodeEntity(data, MaxMetadataBytes)
	requireCode(t, err, "invalid_schema")
	var project Project
	err = decodeYAML([]byte("schema: 1\nproject_id: fixture-project\nnext_issue: 3\nnext_comment: 2\nnext_document: 2\n<<: {project_id: overwritten}\n"), &project)
	requireCode(t, err, "invalid_schema")
}

func TestHistoryRejectsHistoricalForeignIdentity(t *testing.T) {
	for _, field := range []string{"entity", "project", "counter"} {
		t.Run(field, func(t *testing.T) {
			dir := newFixture(t)
			old := fixtureFiles()
			if field == "entity" {
				old["issues/ISSUE-1/issue.md"] = strings.Replace(old["issues/ISSUE-1/issue.md"], "id: ISSUE-1", "id: ISSUE-2", 1)
			} else if field == "project" {
				old[".harness/project.yaml"] = strings.Replace(old[".harness/project.yaml"], "fixture-project", "foreign-project", 1)
			} else {
				old[".harness/project.yaml"] = strings.Replace(old[".harness/project.yaml"], "next_issue: 3", "next_issue: 1", 1)
			}
			first := fixtureCommit(t, dir, old, "")
			fixtureCommit(t, dir, fixtureFiles(), first)
			snap := pinFixture(t, openFixture(t, dir, Limits{}))
			_, err := snap.History(context.Background(), Issue, "ISSUE-1", 2)
			requireCode(t, err, "invalid_schema")
		})
	}
}

func TestHistoryRepeatedBlobBudgetAndHistoricalModes(t *testing.T) {
	dir := newFixture(t)
	files := fixtureFiles()
	first := fixtureCommit(t, dir, files, "")
	second := fixtureCommit(t, dir, files, first)
	// A repeated blob is validated once, not charged once per unrelated commit.
	budget := len(files["issues/ISSUE-1/issue.md"]) + len(files[".harness/project.yaml"])
	snap := pinFixture(t, openFixture(t, dir, Limits{ReadBytes: budget}))
	history, err := snap.History(context.Background(), Issue, "ISSUE-1", 2)
	if err != nil || len(history) != 1 || history[0].Commit != first {
		t.Fatalf("repeated blob history failed: %v", err)
	}
	files["issues/ISSUE-1/issue.md"] += "Changed.\n"
	fixtureCommit(t, dir, files, second)
	_, err = pinFixture(t, openFixture(t, dir, Limits{ReadBytes: budget})).History(context.Background(), Issue, "ISSUE-1", 3)
	requireCode(t, err, "history_unavailable")
	// Mode can change without changing the blob ID, so cached validation cannot
	// replace per-commit mode checks.
	projectTree := fixtureGit(t, dir, "", "ls-tree", first+":.harness")
	unsafeTree := fixtureGit(t, dir, strings.ReplaceAll(projectTree, "100644 blob", "120000 blob")+"\n", "mktree")
	oldTree := fixtureGit(t, dir, "", "rev-parse", first+":.harness")
	root := fixtureGit(t, dir, "", "ls-tree", first)
	rootOID := fixtureGit(t, dir, strings.Replace(root, oldTree, unsafeTree, 1)+"\n", "mktree")
	unsafe := fixtureGit(t, dir, "Unsafe fixture mode\n", "commit-tree", rootOID)
	fixtureCommit(t, dir, fixtureFiles(), unsafe)
	_, err = pinFixture(t, openFixture(t, dir, Limits{})).History(context.Background(), Issue, "ISSUE-1", 2)
	requireCode(t, err, "invalid_schema")
}

func TestRestrictedConfigReplacementRefsAndMissingObjects(t *testing.T) {
	dir := newFixture(t)
	files := fixtureFiles()
	first := fixtureCommit(t, dir, files, "")
	files[".harness/project.yaml"] = strings.ReplaceAll(files[".harness/project.yaml"], "fixture-project", "replacement-project")
	other := fixtureCommit(t, dir, files, "")
	fixtureGit(t, dir, "", "update-ref", "refs/heads/main", first)
	fixtureGit(t, dir, "", "replace", first, other)
	// Local config cannot redirect tree reads into a worktree or execute a helper.
	fixtureGit(t, dir, "", "config", "core.worktree", filepath.Join(t.TempDir(), "unavailable"))
	fixtureGit(t, dir, "", "config", "core.fsmonitor", "unavailable-helper")
	fixtureGit(t, dir, "", "config", "credential.helper", "unavailable-helper")
	global := filepath.Join(t.TempDir(), "hostile-config")
	if err := os.WriteFile(global, []byte("[invalid configuration\n"), 0600); err != nil {
		t.Fatal("fixture write failed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", global)
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.bare=false'")
	s := openFixture(t, dir, Limits{})
	snap := pinFixture(t, s)
	if snap.Project().ID != "fixture-project" {
		t.Fatal("replacement ref/config changed authority")
	}
	_, err := s.run(context.Background(), 128, "cat-file", "blob", strings.Repeat("a", 40))
	requireCode(t, err, "unavailable")
}

func TestOrdinaryDedicatedRepositoryAndCanonicalAlias(t *testing.T) {
	dir := newFixture(t)
	fixtureCommit(t, dir, fixtureFiles(), "")
	fixtureGit(t, dir, "", "config", "core.bare", "false")
	root := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("fixture mkdir failed")
	}
	if err := os.Rename(dir, filepath.Join(root, ".git")); err != nil {
		t.Fatal("fixture move failed")
	}
	s := openFixture(t, root, Limits{})
	if pinFixture(t, s).Project().ID != "fixture-project" {
		t.Fatal("ordinary Git repository read failed")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skip("symlink creation unavailable")
	}
	a := openFixture(t, alias, Limits{})
	if a.dir != s.dir || pinFixture(t, a).Commit() != pinFixture(t, s).Commit() {
		t.Fatal("store alias not canonical")
	}
}
