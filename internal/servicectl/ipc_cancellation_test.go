package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The actual peer owns its socket until EOF or bounded test cleanup. A passing
// cancellation test must not require cleanup to release the caller.
func genericRPCPeer(t *testing.T, handle func(net.Conn)) (*RPCClient, func()) {
	return genericRPCPeerCount(t, 1, handle)
}

func genericRPCPeerCount(t *testing.T, count int, handle func(net.Conn)) (*RPCClient, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("loopback fixture listener unavailable")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var handlers sync.WaitGroup
		defer handlers.Wait()
		for i := 0; i < count; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				closed := make(chan struct{})
				stop := context.AfterFunc(lifetime, func() { _ = conn.Close(); close(closed) })
				defer func() {
					_ = conn.Close()
					if !stop() {
						<-closed
					}
				}()
				handle(conn)
			}()
		}
	}()
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			cancel()
			_ = listener.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("loopback peer did not join")
			}
		})
	}
	t.Cleanup(shutdown)
	return &RPCClient{Network: "tcp", Address: listener.Addr().String()}, shutdown
}

func TestGenericRPCClientCancellationIsPerConnection(t *testing.T) {
	entered := make(chan any, 2)
	reply := make(chan struct{})
	client, stopPeer := genericRPCPeerCount(t, 2, func(conn net.Conn) {
		var request RPCRequest
		if json.NewDecoder(conn).Decode(&request) != nil {
			return
		}
		entered <- request.ID
		if request.Method == "Jobs.Cancel" {
			_, _ = io.Copy(io.Discard, conn)
			return
		}
		<-reply
		_ = json.NewEncoder(conn).Encode(RPCResponse{JSONRPC: jsonrpcVersion, ID: request.ID, Result: 42})
	})
	ctx, cancel := context.WithCancel(context.Background())
	goodCtx, goodCancel := context.WithTimeout(context.Background(), 3*time.Second)
	badDone, goodDone := make(chan error, 1), make(chan error, 1)
	var result int
	go func() { badDone <- client.Call(ctx, "Jobs.Cancel", nil, nil) }()
	go func() { goodDone <- client.Call(goodCtx, "Jobs.List", nil, &result) }()
	badJoined, goodJoined := false, false
	var once sync.Once
	release := func() { once.Do(func() { close(reply) }) }
	t.Cleanup(func() {
		cancel()
		goodCancel()
		release()
		stopPeer()
		for _, owner := range []struct {
			joined bool
			done   <-chan error
		}{{badJoined, badDone}, {goodJoined, goodDone}} {
			if !owner.joined {
				select {
				case <-owner.done:
				case <-time.After(3 * time.Second):
					t.Error("independent caller did not join")
				}
			}
		}
	})
	var ids []any
	for len(ids) < 2 {
		select {
		case id := <-entered:
			ids = append(ids, id)
		case <-goodCtx.Done():
			t.Fatal("independent requests did not reach peer")
		}
	}
	if ids[0] == ids[1] {
		t.Error("concurrent calls reused an RPC id")
	}
	cancel()
	select {
	case err := <-badDone:
		badJoined = true
		if !errors.Is(err, context.Canceled) {
			t.Error("independent cancelled call lost context identity")
		}
	case <-time.After(time.Second):
		t.Fatal("independent cancelled call remained blocked")
	}
	release()
	select {
	case err := <-goodDone:
		goodJoined = true
		if err != nil || result != 42 {
			t.Error("cancellation leaked to another connection")
		}
	case <-goodCtx.Done():
		t.Error("healthy independent call failed to complete")
	}
}

type genericRPCCancelledParam struct{ marshals *atomic.Int32 }

func (p genericRPCCancelledParam) MarshalJSON() ([]byte, error) {
	p.marshals.Add(1)
	return []byte(`{}`), nil
}

func TestGenericRPCClientPrecancelledMemoryDoesNotDispatch(t *testing.T) {
	var marshals atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &RPCClient{Network: "mem", Address: "pre-cancelled-no-owner"}
	err := client.Call(ctx, "Jobs.Cancel", genericRPCCancelledParam{&marshals}, nil)
	if !errors.Is(err, context.Canceled) || marshals.Load() != 0 || client.nextID.Load() != 0 {
		t.Fatal("pre-cancelled memory call performed lookup or serialization")
	}
}

func TestGenericRPCClientReplyCompatibility(t *testing.T) {
	for _, domain := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "domain_error"}[domain], func(t *testing.T) {
			var requests atomic.Int32
			client, _ := genericRPCPeer(t, func(conn net.Conn) {
				var request RPCRequest
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				requests.Add(1)
				response := RPCResponse{JSONRPC: jsonrpcVersion, ID: request.ID, Result: map[string]int{"count": 42}}
				if domain {
					response.Result = nil
					response.Error = &RPCError{Code: -32000, Message: "sanitized domain failure", DiagnosticCode: "cache_busy"}
				}
				_ = json.NewEncoder(conn).Encode(response)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var result struct {
				Count int `json:"count"`
			}
			err := client.Call(ctx, "Jobs.List", nil, &result)
			if domain {
				var coded RPCDomainError
				if !errors.As(err, &coded) || coded.Code != "cache_busy" || coded.Message != "sanitized domain failure" || result.Count != 0 {
					t.Error("generic domain reply compatibility changed")
				}
			} else if err != nil || result.Count != 42 {
				t.Error("generic successful reply compatibility changed")
			}
			if requests.Load() != 1 {
				t.Error("generic reply was not single-request")
			}
		})
	}
}

func TestGenericRPCClientBlockedTransferCancellation(t *testing.T) {
	started, drain := make(chan struct{}), make(chan struct{})
	client, stopPeer := genericRPCPeer(t, func(conn net.Conn) {
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(1024)
		}
		var one [1]byte
		if _, err := conn.Read(one[:]); err != nil {
			return
		}
		close(started)
		<-drain
		_, _ = io.Copy(io.Discard, conn)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- client.Call(ctx, "Maintenance.Enroll", strings.Repeat("x", 8<<20), nil) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		close(drain)
		stopPeer()
		if !joined {
			select {
			case <-returned:
			case <-time.After(3 * time.Second):
				t.Error("blocked-transfer caller did not join")
			}
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("actual transfer did not reach peer")
	}
	cancel()
	select {
	case err := <-returned:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Error("blocked transfer lost cancellation identity")
		}
	case <-time.After(time.Second):
		t.Error("blocked transfer outlived caller cancellation")
	}
}

func TestGenericRPCClientEstablishedCancellation(t *testing.T) {
	for _, method := range []string{"Jobs.List", "Jobs.Cancel", "Maintenance.Enroll", "RepositoryDocs.Status"} {
		for _, reason := range []string{"cancel", "deadline"} {
			t.Run(method+"/"+reason, func(t *testing.T) {
				entered, eof := make(chan struct{}), make(chan struct{})
				var requests atomic.Int32
				client, stopPeer := genericRPCPeer(t, func(conn net.Conn) {
					var request RPCRequest
					if json.NewDecoder(conn).Decode(&request) != nil || request.Method != method {
						return
					}
					requests.Add(1)
					close(entered)
					_, _ = io.Copy(io.Discard, conn)
					close(eof)
				})
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				returned := make(chan error, 1)
				go func() { returned <- client.Call(ctx, method, struct{}{}, nil) }()
				// Failure cleanup releases the peer first, then joins the caller.
				// Success must occur while the silent peer is still awaiting EOF.
				joined := false
				t.Cleanup(func() {
					cancel()
					stopPeer()
					if !joined {
						select {
						case <-returned:
						case <-time.After(3 * time.Second):
							t.Error("generic caller did not join after fixture cleanup")
						}
					}
				})
				select {
				case <-entered:
				case err := <-returned:
					joined = true
					t.Fatalf("call ended before peer decoded request: context classification %t", errors.Is(err, context.DeadlineExceeded))
				case <-ctx.Done():
					t.Fatal("peer did not receive request before deadline")
				}
				want := error(context.DeadlineExceeded)
				if reason == "cancel" {
					want = context.Canceled
					cancel()
				}
				<-ctx.Done()
				select {
				case err := <-returned:
					joined = true
					if !errors.Is(err, want) {
						t.Error("established call lost caller context error identity")
					}
				case <-time.After(250 * time.Millisecond):
					t.Error("established call stayed blocked after caller context ended")
				}
				if joined {
					select {
					case <-eof:
					case <-time.After(time.Second):
						t.Error("client cancellation did not close the established socket")
					}
				}
				if requests.Load() != 1 {
					t.Error("generic call did not remain single-request")
				}
			})
		}
	}
}
