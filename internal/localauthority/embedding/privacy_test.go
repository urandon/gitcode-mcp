package embedding

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

const unsolicitedFixtureMarker = "synthetic_unsolicited_embedding_marker"

func TestUnsolicitedResponseCannotLogProviderBytes(t *testing.T) {
	if os.Getenv("G183_EMBEDDING_LOG_CONTROL") != "child" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal("isolated log control unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestUnsolicitedResponseCannotLogProviderBytes$", "-test.count=1")
		command.Env = []string{"G183_EMBEDDING_LOG_CONTROL=child"}
		output, err := command.CombinedOutput()
		if bytes.Contains(output, []byte(unsolicitedFixtureMarker)) {
			t.Fatal("raw unsolicited provider bytes reached process output")
		}
		if err != nil {
			t.Fatal("isolated unsolicited response control failed")
		}
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("fixture listener unavailable")
	}
	defer listener.Close()
	sent, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		conn, err := listener.Accept()
		if err != nil {
			close(sent)
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, unsolicitedFixtureMarker+"\r\n")
		close(sent)
	}()
	p := prepareFixture(t, "http://"+listener.Addr().String())
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			// Calibration gate: the generic transport starts its reader before
			// registering the request. An unsolicited payload in this interval
			// must not enter the standard library's raw idle-response logger.
			<-sent
			time.Sleep(100 * time.Millisecond)
		},
	})
	_, err = p.Embed(ctx, Document, []string{"synthetic input"})
	if err == nil {
		t.Fatal("unsolicited malformed response was accepted")
	}
	listener.Close()
	<-joined
}

func TestCallerHTTPTraceIsNotAnAdapterCallback(t *testing.T) {
	var callbacks atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprint(w, fixtureInventory(fixtureDigest))
		} else {
			fmt.Fprint(w, fixtureVectors(1))
		}
	}))
	defer s.Close()
	p := prepareFixture(t, s.URL)
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { callbacks.Add(1) }})
	if _, err := p.Embed(ctx, Document, []string{"synthetic input"}); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 0 {
		t.Fatal("ambient HTTP trace callback entered restricted transport")
	}
}

func TestConnectionReadsWaitForRegisteredRequestWrite(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := newRequestConnection(client)
	defer conn.Close()
	client.SetDeadline(time.Now().Add(2 * time.Second))
	server.SetDeadline(time.Now().Add(2 * time.Second))
	read := make(chan error, 1)
	go func() { buffer := make([]byte, 1); _, err := conn.Read(buffer); read <- err }()
	peer := make(chan error, 1)
	go func() {
		if _, err := server.Write([]byte{'s'}); err != nil {
			peer <- err
			return
		}
		buffer := make([]byte, 1)
		_, err := io.ReadFull(server, buffer)
		peer <- err
	}()
	select {
	case <-read:
		t.Fatal("unregistered connection consumed provider bytes")
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := conn.Write([]byte{'r'}); err != nil {
		t.Fatal("registered request write failed")
	}
	if <-read != nil || <-peer != nil {
		t.Fatal("registered request did not join its peer I/O")
	}
}

func TestConnectionCloseBeforeWriteJoinsBlockedReader(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := newRequestConnection(client)
	read := make(chan error, 1)
	go func() { buffer := make([]byte, 1); _, err := conn.Read(buffer); read <- err }()
	if err := conn.Close(); err != nil {
		t.Fatal("connection close failed")
	}
	select {
	case err := <-read:
		if err != net.ErrClosed {
			t.Fatal("blocked read did not report closed connection")
		}
	case <-time.After(time.Second):
		t.Fatal("closed unregistered reader did not join")
	}
}
