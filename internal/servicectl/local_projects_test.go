package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

type localPoisonSource struct{ testSource }

func (s localPoisonSource) Env(key string) string {
	if key == config.EnvToken || key == "GITCODE_MCP_TEST_KEYCHAIN_TOKEN" {
		panic("local hosting entered credential discovery")
	}
	return s.testSource.Env(key)
}

type localPoisonCredentials struct{}

func (localPoisonCredentials) Status(context.Context, config.EffectiveConfig) config.CredentialStatus {
	panic("local hosting probed credentials")
}

func localFixtureGit(t *testing.T, path, input string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + path}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("local Git fixture command failed")
	}
	return strings.TrimSpace(string(out))
}
func localFixtureManager(t *testing.T) (Manager, string) {
	t.Helper()
	m := newTestManager(t, "linux")
	m.Source = localPoisonSource{m.Source.(testSource)}
	m.CredentialReporter = localPoisonCredentials{}
	path, err := config.LocalProjectStorePath(m.Source, "data")
	if err != nil {
		t.Fatal(err)
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		t.Fatal("local fixture parent unavailable")
	}
	localFixtureGit(t, path, "", "init", "--bare", path)
	blob := localFixtureGit(t, path, "schema: 1\nproject_id: lab\nnext_issue: 1\nnext_comment: 1\nnext_document: 1\n", "hash-object", "-w", "--stdin")
	inner := localFixtureGit(t, path, "100644 blob "+blob+"\tproject.yaml\n", "mktree")
	tree := localFixtureGit(t, path, "040000 tree "+inner+"\t.harness\n", "mktree")
	commit := localFixtureGit(t, path, "Fixture\n", "commit-tree", tree)
	localFixtureGit(t, path, "", "update-ref", "refs/heads/main", commit)
	cfg := config.Default()
	cfg.Service.Mode = config.ServiceModeLocal
	cfg.LocalProjects = map[string]config.LocalProjectConfig{"lab": {Backend: "local_git", ProjectID: "lab", StorageRef: "data", Ref: "refs/heads/main"}}
	m.EffectiveConfig = &cfg
	return m, path
}
func runLocalFixture(t *testing.T, m Manager) (*RPCClient, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	client, err := m.Client()
	if err != nil {
		cancel()
		t.Fatal("local fixture client unavailable")
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Error("local fixture daemon failed")
				}
			case <-time.After(3 * time.Second):
				t.Error("local fixture daemon did not join")
			}
		})
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status service.LocalProjectsStatus
		// A ready response performs real bounded Git reads. Give that one
		// request the remaining readiness budget rather than repeatedly killing
		// legitimate work at an unrelated 100ms sub-deadline under test load.
		callCtx, callCancel := context.WithDeadline(context.Background(), deadline)
		err := client.Call(callCtx, "LocalProjects.Status", nil, &status)
		callCancel()
		if err == nil {
			return client, stop
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("local fixture IPC was not published")
	return nil, stop
}
func localCall(t *testing.T, c *RPCClient, method string, req, result any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Call(ctx, method, req, result); err != nil {
		t.Fatal(err)
	}
}
func localCode(t *testing.T, err error, code string) {
	t.Helper()
	var e interface{ DiagnosticCode() string }
	if !errors.As(err, &e) || e.DiagnosticCode() != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestLocalProjectsPrivateIPCWriteReadRestartReplay(t *testing.T) {
	m, path := localFixtureManager(t)
	client, stop := runLocalFixture(t, m)
	var status service.LocalProjectsStatus
	localCall(t, client, "LocalProjects.Status", nil, &status)
	if len(status.Projects) != 1 || !status.Projects[0].Readable || status.Projects[0].ReadOnly {
		t.Fatal("local readiness mismatch")
	}
	create := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "Research body.\n", ExpectedHead: status.Projects[0].Commit, WriteMode: "local", IdempotencyKey: "original"}}
	var created localgit.MutationResult
	localCall(t, client, "LocalProjects.Mutate", create, &created)
	if created.Entity.Metadata.ID != "ISSUE-1" || created.Replayed {
		t.Fatal("private create failed")
	}
	var read localgit.Entity
	localCall(t, client, "LocalProjects.Get", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: "ISSUE-1"}, &read)
	if read.Body != created.Entity.Body || read.Citation != created.Entity.Citation {
		t.Fatal("private get citation mismatch")
	}
	var list []localgit.Entity
	localCall(t, client, "LocalProjects.List", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, Limit: 8}, &list)
	if len(list) != 1 {
		t.Fatal("private list mismatch")
	}
	update := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Answered", Body: "Later body.\n", State: "closed", ExpectedRevision: read.Citation.Blob, WriteMode: "local", IdempotencyKey: "later"}}
	var updated localgit.MutationResult
	localCall(t, client, "LocalProjects.Mutate", update, &updated)
	var history []localgit.Citation
	localCall(t, client, "LocalProjects.History", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: "ISSUE-1", MaxCommits: 8}, &history)
	if len(history) != 2 {
		t.Fatal("private history mismatch")
	}
	var health ServiceHealth
	localCall(t, client, "Service.Health", nil, &health)
	if !health.Healthy || health.Status != StatusRunning {
		t.Fatal("existing lifecycle health incompatibility")
	}
	paths, _ := m.ResolvePaths()
	for _, file := range []string{paths.JobsPath, paths.RegistryPath} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatal("local startup entered remote maintenance graph")
		}
	}
	if info, err := os.Stat(paths.SocketPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private socket mode mismatch")
	}
	stop()
	client, stop = runLocalFixture(t, m)
	defer stop()
	var replay localgit.MutationResult
	localCall(t, client, "LocalProjects.Mutate", create, &replay)
	if !replay.Replayed || replay.Entity.Citation != created.Entity.Citation || replay.Entity.Body != created.Entity.Body {
		t.Fatal("restart lost original receipt result")
	}
	if head := localFixtureGit(t, path, "", "rev-parse", "refs/heads/main"); head != updated.Entity.Citation.Commit {
		t.Fatal("restart replay published a duplicate")
	}
}

func TestLocalProjectsAliasReadOnlyRoutingAndSanitizedFailures(t *testing.T) {
	m, path := localFixtureManager(t)
	alias, err := config.LocalProjectStorePath(m.Source, "alias")
	if err != nil {
		t.Fatal(err)
	}
	if os.Symlink(path, alias) != nil {
		t.Skip("canonical alias fixture unavailable")
	}
	p := m.EffectiveConfig.LocalProjects["lab"]
	p.StorageRef = "alias"
	p.ReadOnly = true
	m.EffectiveConfig.LocalProjects["alias"] = p
	client, _ := runLocalFixture(t, m)
	var status service.LocalProjectsStatus
	localCall(t, client, "LocalProjects.Status", nil, &status)
	if len(status.Projects) != 2 || !status.Projects[0].ReadOnly || status.Projects[0].Commit != status.Projects[1].Commit {
		t.Fatal("canonical alias policy mismatch")
	}
	req := service.LocalProjectMutation{Project: "alias", Mutation: localgit.Mutation{Operation: "create_issue", Title: "No write", ExpectedHead: status.Projects[0].Commit, WriteMode: "local", IdempotencyKey: "ro"}}
	err = client.Call(context.Background(), "LocalProjects.Mutate", req, nil)
	localCode(t, err, "local_git_read_only")
	req.Project = "lab"
	req.Mutation.ExpectedHead = strings.Repeat("f", 40)
	err = client.Call(context.Background(), "LocalProjects.Mutate", req, nil)
	localCode(t, err, "local_git_write_conflict")
	localCall(t, client, "LocalProjects.Status", nil, &status)
	if status.Projects[0].LastConflict != "local_git_write_conflict" || status.Projects[1].LastConflict != status.Projects[0].LastConflict {
		t.Fatal("aliases did not share outcome observation")
	}
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), path) || strings.Contains(string(encoded), "Research body") || strings.Contains(string(encoded), "storage_ref") {
		t.Fatal("status leaked private store or content")
	}
	for _, test := range []struct {
		method string
		params any
		code   string
	}{
		{"LocalProjects.Get", service.LocalProjectQuery{Project: "unknown", Kind: localgit.Issue, ID: "ISSUE-1"}, "local_git_unknown_project"},
		{"LocalProjects.Get", map[string]any{"project": "lab", "path": "private-coordinate"}, "local_git_invalid_argument"},
		{"LocalProjects.RemoteSync", nil, "local_git_unsupported_capability"},
		{"Jobs.StartFake", nil, "local_git_unsupported_capability"},
		{"Admin.Open", nil, "local_git_unsupported_capability"},
		{"Service.Doctor", nil, "local_git_unsupported_capability"},
	} {
		err := client.Call(context.Background(), test.method, test.params, nil)
		localCode(t, err, test.code)
		if strings.Contains(err.Error(), "private-coordinate") || strings.Contains(err.Error(), path) {
			t.Fatal("routing error leaked request/store")
		}
	}
}

func TestLocalProjectsRejectInvalidAuthorityAndHosting(t *testing.T) {
	m, _ := localFixtureManager(t)
	for _, edit := range []func(*Manager){
		func(m *Manager) { m.AdminAutoStart = true },
		func(m *Manager) {
			m.Source = localPoisonSource{testSource{env: map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "tcp"}}}
		},
		func(m *Manager) {
			m.EffectiveConfig.LocalProjects["lab"] = config.LocalProjectConfig{Backend: "remote", ProjectID: "lab", StorageRef: "data", Ref: "refs/heads/main"}
		},
	} {
		copy := m
		cfg := *m.EffectiveConfig
		cfg.LocalProjects = map[string]config.LocalProjectConfig{"lab": m.EffectiveConfig.LocalProjects["lab"]}
		copy.EffectiveConfig = &cfg
		edit(&copy)
		if err := copy.Run(context.Background()); err == nil {
			t.Fatal("invalid local hosting accepted")
		}
	}
	alias := m.EffectiveConfig.LocalProjects["lab"]
	alias.Ref = "refs/heads/other"
	m.EffectiveConfig.LocalProjects["alias"] = alias
	_, err := service.NewLocalProjects(context.Background(), m.EffectiveConfig.LocalProjects, func(ref string) (string, error) { return config.LocalProjectStorePath(m.Source, ref) })
	if err == nil {
		t.Fatal("canonical store admitted inconsistent ref aliases")
	}
}

func TestLocalProjectsInflightGitCancellationPropagatesThroughUnix(t *testing.T) {
	m, path := localFixtureManager(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("Git executable unavailable")
	}
	wrapper := t.TempDir()
	entered := filepath.Join(wrapper, "entered")
	// The wrapper blocks the real candidate commit creation using shell builtins
	// only. Cancellation kills/joins that one owned process, with no child sleeper.
	script := "#!/bin/sh\ncase \"$*\" in *commit-tree*) printf entered > '" + entered + "'; while :; do :; done ;; esac\nexec '" + realGit + "' \"$@\"\n"
	if os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0700) != nil {
		t.Fatal("Git cancellation fixture unavailable")
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+originalPath)
	client, stop := runLocalFixture(t, m)
	var status service.LocalProjectsStatus
	localCall(t, client, "LocalProjects.Status", nil, &status)
	req := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "create_issue", Title: "Cancelled", ExpectedHead: status.Projects[0].Commit, WriteMode: "local", IdempotencyKey: "cancelled"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Call(ctx, "LocalProjects.Mutate", req, nil) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(entered); err != nil {
		cancel()
		stop()
		t.Fatal("inflight request did not reach Git candidate commit")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("caller cancellation identity lost")
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not return")
	}
	// Stop joins the server handler and its cancelled Git command before proving
	// authority. No test cleanup is needed to unblock the caller itself.
	stop()
	if head := localFixtureGit(t, path, "", "rev-parse", "refs/heads/main"); head != status.Projects[0].Commit {
		t.Fatal("cancelled candidate published")
	}
	t.Setenv("PATH", originalPath)
	client, stop = runLocalFixture(t, m)
	defer stop()
	var retry localgit.MutationResult
	localCall(t, client, "LocalProjects.Mutate", req, &retry)
	if retry.Replayed || retry.Entity.Metadata.ID != "ISSUE-1" {
		t.Fatal("cancelled candidate consumed a durable ID/receipt")
	}
}
