//go:build darwin || linux

package localauthority_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/servicectl"
)

const fixtureProject authority.UUID = "44444444-4444-4444-8444-444444444444"

type ipcFixture struct {
	client        *servicectl.RPCClient
	socket, store string
	core          *localauthority.Coordinator
	prepared      *localauthority.PreparedRegistry
	stop          context.CancelFunc
	done          chan error
}

func privateTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "authority4-")
	if err != nil || os.Chmod(dir, 0700) != nil {
		t.Fatal("private native fixture unavailable")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal("private native fixture identity unavailable")
	}
	t.Cleanup(func() {
		if os.RemoveAll(dir) != nil {
			t.Error("private native fixture cleanup failed")
		}
	})
	return dir
}

func newIPCFixture(t *testing.T) ipcFixture {
	t.Helper()
	r, err := localauthority.NewFixtureRegistry(privateTemp(t))
	if err != nil {
		t.Fatal("isolated machine registry unavailable")
	}
	dir := privateTemp(t)
	m := authority.Manifest{Schema: 1, ID: fixtureProject, Slug: "local-fixture", Authority: authority.Local, Entities: []authority.EntityKind{authority.Issue, authority.Comment, authority.Label, authority.Milestone, authority.Document, authority.Link}, Network: authority.NetworkPolicy{DataEgress: "deny", ControlTransport: "ipc_only"}, Search: authority.SearchIntent{Default: "hybrid", Fallback: "full_text"}}
	s, err := localauthority.Initialize(context.Background(), r, dir, m)
	if err != nil {
		t.Fatal("explicit native authority fixture init failed")
	}
	if _, err = s.SetupKnowledge(context.Background(), fixtureProject, "synthetic-native-setup"); err != nil || s.Close() != nil {
		t.Fatal("explicit native knowledge fixture init failed")
	}
	p, err := r.PrepareRuntime()
	if err != nil {
		t.Fatal("native authority preparation failed")
	}
	ctx, stop := context.WithCancel(context.Background())
	c, err := localauthority.NewStrictCoordinator(ctx, p, []localauthority.ProjectBinding{{ProjectID: fixtureProject, Directory: dir}})
	if err != nil {
		stop()
		_ = p.Close()
		t.Fatal("native coordinator admission failed")
	}
	server, err := servicectl.NewAuthorityServer(c)
	if err != nil {
		stop()
		t.Fatal("dedicated native server unavailable")
	}
	path := filepath.Join(privateTemp(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil || os.Chmod(path, 0600) != nil {
		stop()
		t.Fatal("private native socket unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, l) }()
	f := ipcFixture{client: &servicectl.RPCClient{Network: "unix", Address: path}, socket: path, store: dir, core: c, prepared: p, stop: stop, done: done}
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Error("native server shutdown failed")
			}
		case <-time.After(time.Second):
			t.Error("native server handler not joined")
		}
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if c.Close(shutdown) != nil || p.Close() != nil {
			t.Error("native ownership teardown did not join")
		}
	})
	return f
}

func nativeMutation(key, title string) authority.MutationRequest {
	payload, _ := json.Marshal(authority.IssuePayload{Title: title, Body: "typed local evidence", State: "open"})
	return authority.MutationRequest{Intent: authority.WriteIntent{ProjectID: fixtureProject, WriteMode: "local", IdempotencyKey: key}, Operation: "issue.create", Payload: payload}
}

func TestDedicatedAuthorityActualPrivateRoundTrip(t *testing.T) {
	f := newIPCFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var caps authority.Negotiation
	if f.client.Call(ctx, "AuthorityV1.Negotiate", authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}, &caps) != nil || !caps.Available || len(caps.Methods) != 10 {
		t.Fatal("dedicated core did not negotiate actual methods")
	}
	m := nativeMutation("synthetic-native-create", "Real canonical issue")
	var receipt authority.MutationReceipt
	if f.client.Call(ctx, "AuthorityV1.Mutate", m, &receipt) != nil || receipt.Validate() != nil {
		t.Fatal("actual private mutation failed")
	}
	var replay authority.MutationReceipt
	if f.client.Call(ctx, "AuthorityV1.Mutate", m, &replay) != nil || !replay.Replayed || replay.Citation != receipt.Citation {
		t.Fatal("private replay changed confirmed authority")
	}
	var read authority.ReadResult
	if f.client.Call(ctx, "AuthorityV1.Read", authority.ReadRequest{ProjectID: fixtureProject, Kind: authority.Issue, Operation: "get", EntityID: receipt.Citation.EntityID, Revision: 1}, &read) != nil || read.Validate() != nil || len(read.Entities) != 1 {
		t.Fatal("private citation read failed")
	}
	var search localauthority.SearchResult
	searchErr := f.client.Call(ctx, "AuthorityV1.Search", localauthority.SearchRequest{ProjectID: fixtureProject, Query: "canonical issue", Limit: 8}, &search)
	if searchErr != nil || search.Validate() != nil || search.RequestedMode != "hybrid" || search.EffectiveMode != "full_text" || search.FallbackReason != "policy_denied" || len(search.Hits) != 1 || search.Hits[0].Citation != receipt.Citation {
		t.Fatalf("private hybrid fallback failed: %v, hits %d", searchErr, len(search.Hits))
	}
	var status authority.ProjectStatus
	if f.client.Call(ctx, "AuthorityV1.Read", authority.ReadRequest{ProjectID: fixtureProject, Operation: "status"}, &status) != nil || status.Validate() != nil || status.AuthorityGeneration != 1 {
		t.Fatal("private status changed identity or generation")
	}
	// Documents use the separate Git contract, never generic SQL byte storage.
	payload, _ := json.Marshal(authority.DocumentPayload{Title: "Native document", Markdown: "# Git-only-native-byte-sentinel"})
	doc := authority.DocumentMutationRequest{Schema: 1, Intent: authority.DocumentWriteIntent{ProjectID: fixtureProject, WriteMode: "local", IdempotencyKey: "synthetic-native-doc", ExpectedRefOID: authority.EmptyKnowledgeRef}, Operation: "document.create", Payload: payload}
	var written authority.DocumentMutationResult
	if f.client.Call(ctx, "AuthorityV1.MutateDocument", doc, &written) != nil || written.Validate() != nil {
		t.Fatal("private Git document mutation failed")
	}
	if f.client.Call(ctx, "AuthorityV1.Read", authority.ReadRequest{ProjectID: fixtureProject, Kind: authority.Document, Operation: "get", EntityID: written.Receipt.Citation.EntityID, Revision: 1}, &read) != nil || !strings.Contains(string(read.Entities[0].Payload), "Git-only-native-byte-sentinel") {
		t.Fatal("private immutable Git byte read failed")
	}
	for _, name := range []string{"tracker.db", "tracker.db-wal"} {
		body, err := os.ReadFile(filepath.Join(f.store, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal("native SQL sentinel inspection failed")
		}
		if bytes.Contains(body, []byte("Git-only-native-byte-sentinel")) {
			t.Fatal("document bytes persisted in SQL")
		}
	}
}

func TestDedicatedAuthorityRejectsRemoteMethodsAndUntrustedSelectors(t *testing.T) {
	f := newIPCFixture(t)
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"Service.Status","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"AuthorityV1.Read","params":{"project_id":"44444444-4444-4444-8444-444444444444","operation":"status","path":"synthetic-private-coordinate"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"AuthorityV1.Read","params":{"project_id":"44444444-4444-4444-8444-444444444444","project_id":"55555555-5555-4555-8555-555555555555","operation":"status"}}`,
		`{"jsonrpc":"2.0","id":{"secret":"synthetic-private-coordinate"},"method":"AuthorityV1.Read","params":{}}`,
	} {
		conn, err := net.DialTimeout("unix", f.socket, time.Second)
		if err != nil {
			t.Fatal("native rejection socket unavailable")
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(conn, raw+"\n")
		body, err := io.ReadAll(io.LimitReader(conn, (512<<10)+8192))
		_ = conn.Close()
		if err != nil || bytes.Contains(body, []byte("synthetic-private-coordinate")) || bytes.Contains(body, []byte(f.store)) {
			t.Fatal("native refusal leaked raw selector or failed")
		}
		var response servicectl.RPCResponse
		if json.Unmarshal(body, &response) != nil || response.Error == nil || response.Result != nil {
			t.Fatal("unknown/invalid native request admitted")
		}
	}
	var status authority.ProjectStatus
	if f.client.Call(context.Background(), "AuthorityV1.Read", authority.ReadRequest{ProjectID: fixtureProject, Operation: "status"}, &status) != nil || status.AuthorityGeneration != 0 {
		t.Fatal("rejected requests changed canonical authority")
	}
}

func TestDedicatedAuthoritySilentConnectionsAreBoundedAndJoined(t *testing.T) {
	f := newIPCFixture(t)
	var conns []net.Conn
	for i := 0; i < 40; i++ {
		conn, err := net.DialTimeout("unix", f.socket, time.Second)
		if err != nil {
			t.Fatal("silent peer fixture dial failed")
		}
		conns = append(conns, conn)
	}
	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		go func(conn net.Conn) {
			defer wg.Done()
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, _ = io.Copy(io.Discard, conn)
		}(conn)
	}
	f.stop()
	wg.Wait()
	// Fixture teardown independently awaits Serve and coordinator ownership.
}

func TestDedicatedAuthorityClientRejectsAnotherValidReceiptEvenWhenDiscarded(t *testing.T) {
	f := newIPCFixture(t)
	m := nativeMutation("synthetic-confirmation-key", "Exact original intent")
	var original authority.MutationReceipt
	if f.client.Call(context.Background(), "AuthorityV1.Mutate", m, &original) != nil {
		t.Fatal("confirmed receipt fixture failed")
	}
	var caps authority.Negotiation
	if f.client.Call(context.Background(), "AuthorityV1.Negotiate", authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}, &caps) != nil {
		t.Fatal("capability fixture failed")
	}
	for _, field := range []string{"key", "intent", "body"} {
		t.Run(field, func(t *testing.T) {
			bad := original
			if field == "key" {
				bad.IdempotencyKeyHash = strings.Repeat("0", 64)
			}
			if field == "intent" {
				bad.IntentHash = strings.Repeat("0", 64)
			}
			request := m
			if field == "body" {
				request = nativeMutation(m.Intent.IdempotencyKey, "Different caller intent")
			}
			if bad.Validate() != nil {
				t.Fatal("tamper control was not otherwise a valid receipt")
			}
			path := filepath.Join(privateTemp(t), "s")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal("bounded private response control unavailable")
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for i := 0; i < 2; i++ {
					conn, err := listener.AcceptUnix()
					if err != nil {
						return
					}
					_ = conn.SetDeadline(time.Now().Add(time.Second))
					var rpc servicectl.RPCRequest
					if json.NewDecoder(io.LimitReader(conn, 512<<10)).Decode(&rpc) != nil {
						_ = conn.Close()
						return
					}
					var result any = bad
					if rpc.Method == "AuthorityV1.Negotiate" {
						result = caps
					}
					_ = json.NewEncoder(conn).Encode(servicectl.RPCResponse{JSONRPC: "2.0", ID: rpc.ID, Result: result})
					_ = conn.Close()
				}
			}()
			client := servicectl.RPCClient{Network: "unix", Address: path}
			err = client.Call(ctx, "AuthorityV1.Mutate", request, nil)
			_ = listener.Close()
			<-done
			var typed authority.Error
			if !errors.As(err, &typed) || typed.Code != authority.InvalidContract {
				t.Fatal("client confirmed unrelated receipt with nil result target")
			}
		})
	}
}
