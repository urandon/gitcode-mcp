package servicectl

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

func TestLocalProjectsMemoryShutdownJoinsOwnedMutation(t *testing.T) {
	m, path := localFixtureManager(t)
	source := m.Source.(localPoisonSource)
	source.env["GITCODE_MCP_SERVICE_NETWORK"] = "mem"
	source.env["GITCODE_MCP_SERVICE_ADDRESS"] = "local-join-" + source.configDir
	m.Source = source
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("fixture Git unavailable")
	}
	wrapper := t.TempDir()
	entered := filepath.Join(wrapper, "entered")
	script := "#!/bin/sh\ncase \"$*\" in *commit-tree*) printf entered > '" + entered + "'; while :; do :; done ;; esac\nexec '" + realGit + "' \"$@\"\n"
	if os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0700) != nil {
		t.Fatal("wrapper unavailable")
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+originalPath)
	client, stop := runLocalFixture(t, m)
	var status service.LocalProjectsStatus
	localCall(t, client, "LocalProjects.Status", nil, &status)
	req := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "create_issue", Title: "Join proof", ExpectedHead: status.Projects[0].Commit, WriteMode: "local", IdempotencyKey: "joined"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Call(ctx, "LocalProjects.Mutate", req, nil) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("mutation did not reach owned Git work")
		}
		time.Sleep(time.Millisecond)
	}
	stop() // The caller remains active: only daemon shutdown can cancel its Git work.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled mutation acknowledged success")
		}
	case <-time.After(200 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("daemon returned before joining an admitted mutation")
	}
	if head := localFixtureGit(t, path, "", "rev-parse", "refs/heads/main"); head != status.Projects[0].Commit {
		t.Fatal("cancelled candidate published")
	}
	t.Setenv("PATH", originalPath)
	client, stop = runLocalFixture(t, m)
	defer stop()
	var retry localgit.MutationResult
	localCall(t, client, "LocalProjects.Mutate", req, &retry)
	if retry.Replayed || retry.Entity.Metadata.ID != "ISSUE-1" {
		t.Fatal("cancelled work consumed a durable receipt or ID")
	}
}

func localResultPeer(t *testing.T, result json.RawMessage) *RPCClient {
	t.Helper()
	// Short relative socket path avoids platform AF_UNIX path-size dependence.
	f, err := os.CreateTemp(".", "local-peer-")
	if err != nil {
		t.Fatal("peer path unavailable")
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal("peer listener unavailable")
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		listener.Close()
		os.Remove(path)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("peer did not join")
		}
	})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(time.Second))
		var req RPCRequest
		if json.NewDecoder(conn).Decode(&req) != nil {
			return
		}
		json.NewEncoder(conn).Encode(RPCResponse{JSONRPC: jsonrpcVersion, ID: req.ID, Result: result})
	}()
	return &RPCClient{Network: "unix", Address: path}
}

func localResponseFixture() localgit.Entity {
	return localgit.Entity{Metadata: localgit.Metadata{Schema: 1, Kind: localgit.Issue, ID: "ISSUE-1", Title: "Fixture", State: "open"}, Body: "Body", Citation: localgit.Citation{ProjectID: "lab", Kind: localgit.Issue, ID: "ISSUE-1", Commit: strings.Repeat("a", 40), Blob: strings.Repeat("b", 40), Path: "issues/ISSUE-1/issue.md"}}
}

func TestLocalProjectsMalformedMutationCannotAcknowledge(t *testing.T) {
	e := localResponseFixture()
	entity, _ := json.Marshal(e)
	for _, raw := range []string{"{}", "null", `{"entity":{},"replayed":false}`, `{"entity":` + string(entity) + `}`, `{"entity":` + strings.Replace(string(entity), `"body":"Body",`, "", 1) + `,"replayed":false}`} {
		for _, discard := range []bool{false, true} {
			client := localResultPeer(t, json.RawMessage(raw))
			prior := localgit.MutationResult{Replayed: true, Entity: localgit.Entity{Body: "retained prior result"}}
			before := prior
			var result any = &prior
			if discard {
				result = nil
			}
			err := client.Call(context.Background(), "LocalProjects.Mutate", service.LocalProjectMutation{Project: "lab"}, result)
			localCode(t, err, "local_git_publication_ambiguous")
			if !reflect.DeepEqual(prior, before) {
				t.Fatal("invalid reply changed caller result")
			}
		}
	}
}

func TestLocalProjectsClosedResultAndFreshAssignment(t *testing.T) {
	e := localResponseFixture()
	raw, _ := json.Marshal(e)
	for _, invalid := range []string{
		strings.TrimSuffix(string(raw), "}") + `,"unknown_field":"fixture"}`,
		strings.Replace(string(raw), `"body":"Body"`, `"body":null`, 1),
		strings.Replace(string(raw), `"body":"Body",`, "", 1),
		strings.Replace(string(raw), `"commit":"`+e.Citation.Commit+`"`, `"commit":"short"`, 1),
	} {
		client := localResultPeer(t, json.RawMessage(invalid))
		var got localgit.Entity
		err := client.Call(context.Background(), "LocalProjects.Get", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: "ISSUE-1"}, &got)
		localCode(t, err, "local_git_invalid_response")
	}
	client := localResultPeer(t, raw)
	got := localgit.Entity{Metadata: localgit.Metadata{Labels: []string{"old"}, Milestone: "old"}, Body: "old"}
	localCall(t, client, "LocalProjects.Get", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: "ISSUE-1"}, &got)
	if !reflect.DeepEqual(got, e) {
		t.Fatal("successful result merged stale optional fields")
	}
	mutation, _ := json.Marshal(localgit.MutationResult{Entity: e})
	localCall(t, localResultPeer(t, mutation), "LocalProjects.Mutate", service.LocalProjectMutation{Project: "lab"}, nil)
}
