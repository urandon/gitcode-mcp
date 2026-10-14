package localgit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gitcode-mcp/internal/repositorydocs"
	"gopkg.in/yaml.v3"
)

// Initialize creates a new dedicated store only. It never adopts, replaces or
// removes an existing destination. Machine-local setup owns the path selection.
func Initialize(ctx context.Context, path, projectID, ref string) (_ *Store, err error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	project := Project{Schema: SchemaVersion, ID: projectID, NextIssue: 1, NextComment: 1, NextDocument: 1}
	if validateProject(project) != nil || !filepath.IsAbs(path) || !strings.HasPrefix(ref, "refs/heads/") {
		return nil, fail("invalid_argument", "project")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fail("unavailable", "git")
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return nil, fail("unavailable", "store")
	}
	if err = os.Mkdir(path, 0700); err != nil {
		if os.IsExist(err) {
			return nil, fail("write_conflict", "store_exists")
		}
		return nil, fail("unavailable", "store")
	}
	// Only this fresh, exclusively claimed directory is removed on failure.
	// Once initialization succeeds the Git repository survives registration errors.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(path)
		}
	}()
	limits, _ := normalizeLimits(Limits{})
	s := &Store{dir: path, git: git, limits: limits}
	if os.Mkdir(filepath.Join(path, "objects"), 0700) != nil {
		return nil, fail("unavailable", "store")
	}
	if _, err = s.run(ctx, 1024, "init", "--bare", "--template=", path); err != nil {
		return nil, err
	}
	if err = s.ValidateLocalBranch(ctx, ref); err != nil {
		return nil, err
	}
	data, _ := yaml.Marshal(project)
	blob, err := s.writeBlob(ctx, data)
	if err != nil {
		return nil, err
	}
	tree, err := s.patchTree(ctx, "", map[string]repositorydocs.TreeEntry{".harness/project.yaml": {Mode: "100644", Type: "blob", OID: blob}})
	if err != nil {
		return nil, err
	}
	out, err := s.runInput(ctx, 128, []byte("Initialize local research project\n"), "commit-tree", tree)
	if err != nil {
		return nil, err
	}
	commit := strings.TrimSpace(string(out))
	if !oidValid(commit) {
		return nil, fail("unavailable", "commit")
	}
	if _, err = s.run(ctx, 128, "update-ref", "--no-deref", ref, commit, strings.Repeat("0", len(commit))); err != nil {
		return nil, err
	}
	if _, err = s.run(ctx, 128, "symbolic-ref", "HEAD", ref); err != nil {
		return nil, err
	}
	if _, err = s.Snapshot(ctx, ref); err != nil {
		return nil, err
	}
	return s, nil
}
