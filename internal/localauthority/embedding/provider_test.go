package embedding

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureModel = "example-embed:small"
const fixtureDigest = "1111111111111111111111111111111111111111111111111111111111111111"

func fixtureSpec(endpoint string) Spec {
	return Spec{Endpoint: endpoint, Model: fixtureModel, Digest: fixtureDigest, Dimensions: 2, Evidence: TrustedFixedLocalModel}
}

func fixtureInventory(digest string) string {
	return `{"models":[{"name":"` + fixtureModel + `","model":"` + fixtureModel + `","modified_at":"2026-01-01T00:00:00Z","size":1234,"digest":"` + digest + `","details":{"parent_model":"","format":"gguf","family":"example","families":["example"],"parameter_size":"small","quantization_level":"F16"}}]}`
}

func fixtureVectors(count int) string {
	items := make([]string, count)
	for i := range items {
		items[i] = `[3,4]`
	}
	return `{"model":"` + fixtureModel + `","embeddings":[` + strings.Join(items, ",") + `],"total_duration":1,"load_duration":0,"prompt_eval_count":2}`
}

func prepareFixture(t *testing.T, endpoint string) *Provider {
	t.Helper()
	p, err := Prepare(fixtureSpec(endpoint))
	if err != nil {
		t.Fatal("fixture preparation failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if p.Close(ctx) != nil {
			t.Error("fixture provider did not join")
		}
	})
	return p
}

func requireCode(t *testing.T, err error, code Code) {
	t.Helper()
	var finite Error
	if !errors.As(err, &finite) || finite.Code != code {
		t.Fatalf("expected %s, got finite error %v", code, err)
	}
	// Every actual transport error is content-free. Do not print private request
	// text or raw provider responses even when a regression fails this assertion.
	if finite.Error() != "local embeddings: "+string(code) || err.Error() != finite.Error() {
		t.Fatal("error escaped its finite contract")
	}
}

func TestPreparePureAndStrict(t *testing.T) {
	var requests atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer s.Close()
	p := prepareFixture(t, s.URL)
	if requests.Load() != 0 || p.inFlight.Load() != 0 {
		t.Fatal("preparation performed provider I/O")
	}
	badEndpoints := []string{
		"http://localhost:11434", "https://127.0.0.1:11434", "http://192.0.2.1:11434", "http://127.0.0.1", "http://127.0.0.1:0",
		"http://127.0.0.1:65536", "http://127.0.0.1:011434", "http://user:secret@127.0.0.1:11434", "http://127.0.0.1:11434/",
		"http://127.0.0.1:11434/api", "http://127.0.0.1:11434?secret=x", "http://127.0.0.1:11434?", "http://127.0.0.1:11434#secret",
		"http://127.0.0.1:11434#", "HTTP://127.0.0.1:11434", "http://127.0.0.1:11434/" + strings.Repeat("x", 129),
		"http://[::1%25test]:11434", "http://[2001:db8::1]:11434", "http://127.1:11434",
	}
	for i, endpoint := range badEndpoints {
		t.Run(fmt.Sprint("endpoint-", i), func(t *testing.T) {
			_, err := Prepare(fixtureSpec(endpoint))
			requireCode(t, err, InvalidSpec)
		})
	}
	for _, change := range []func(*Spec){
		func(s *Spec) { s.Model = "example" }, func(s *Spec) { s.Model = "example:cloud" }, func(s *Spec) { s.Model = "example:small\nAuthorization" },
		func(s *Spec) { s.Model = strings.Repeat("a", 129) + ":x" }, func(s *Spec) { s.Digest = "bad" }, func(s *Spec) { s.Digest = strings.Repeat("A", 64) },
		func(s *Spec) { s.Dimensions = 0 }, func(s *Spec) { s.Dimensions = 4097 }, func(s *Spec) { s.Evidence = "" },
	} {
		spec := fixtureSpec(s.URL)
		change(&spec)
		_, err := Prepare(spec)
		requireCode(t, err, InvalidSpec)
	}
	if requests.Load() != 0 {
		t.Fatal("invalid preparation performed I/O")
	}
}

func TestEmbedClosedContractFreshConnectionsAndNormalization(t *testing.T) {
	var mu sync.Mutex
	var methods, paths, addresses []string
	var received []string
	var invalid bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		addresses = append(addresses, r.RemoteAddr)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Accept-Encoding") != "" || r.URL.RawQuery != "" || !r.Close {
			invalid = true
		}
		if r.URL.Path == "/api/tags" && r.Method == http.MethodGet {
			fmt.Fprint(w, fixtureInventory(fixtureDigest))
			return
		}
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			invalid = true
			w.WriteHeader(404)
			return
		}
		var request map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request) != 3 || string(request["model"]) != `"`+fixtureModel+`"` || string(request["truncate"]) != "false" || json.Unmarshal(request["input"], &received) != nil {
			invalid = true
		}
		fmt.Fprint(w, fixtureVectors(len(received)))
	}))
	defer s.Close()
	p := prepareFixture(t, s.URL)
	inputs := []string{"synthetic document", "second document"}
	batch, err := p.Embed(context.Background(), Document, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batch.Binding, Binding{fixtureDigest, 2, InstructionPolicy, TrustedFixedLocalModel}) || len(batch.Vectors) != 2 {
		t.Fatal("binding/cardinality changed")
	}
	for _, vector := range batch.Vectors {
		if len(vector) != 2 || math.Abs(float64(vector[0])-.6) > 1e-6 || math.Abs(float64(vector[1])-.8) > 1e-6 {
			t.Fatal("finite normalized vector missing")
		}
	}
	mu.Lock()
	if invalid || !reflect.DeepEqual(methods, []string{"GET", "POST", "GET"}) || !reflect.DeepEqual(paths, []string{"/api/tags", "/api/embed", "/api/tags"}) || !reflect.DeepEqual(received, inputs) || addresses[0] == addresses[1] || addresses[0] == addresses[2] || addresses[1] == addresses[2] {
		t.Error("restricted wire contract changed")
	}
	mu.Unlock()
	if _, err := p.Embed(context.Background(), Query, []string{"synthetic query"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if !reflect.DeepEqual(received, []string{queryInstruction + "synthetic query"}) {
		t.Error("query policy missing")
	}
	mu.Unlock()
	if _, err := p.Smoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if !reflect.DeepEqual(received, []string{smokeInput}) {
		t.Error("smoke sent nonconstant input")
	}
	mu.Unlock()
}

func TestInputLimitsBeforeAnyIO(t *testing.T) {
	var requests atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer s.Close()
	p := prepareFixture(t, s.URL)
	cases := []struct {
		purpose Purpose
		inputs  []string
		code    Code
	}{
		{Document, nil, InvalidInput}, {Document, make([]string, 9), InvalidInput}, {Document, []string{" "}, InvalidInput},
		{Document, []string{string([]byte{0xff})}, InvalidInput}, {Purpose("unprepared"), []string{"text"}, InvalidInput},
		{Document, []string{strings.Repeat("a", 4097)}, InvalidInput},
		{Query, []string{strings.Repeat("a", 4096-len(queryInstruction)+1)}, InvalidInput},
		{Document, []string{strings.Repeat("\x00", 4096), strings.Repeat("\x00", 4096), strings.Repeat("\x00", 4096)}, RequestTooLarge},
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, err := p.Embed(context.Background(), c.purpose, c.inputs)
			requireCode(t, err, c.code)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Embed(ctx, Document, []string{"text"})
	requireCode(t, err, Canceled)
	if !errors.Is(err, context.Canceled) || requests.Load() != 0 {
		t.Fatal("invalid/canceled request performed I/O")
	}
}

func TestModelCheckBeforeAndAfterEmbedding(t *testing.T) {
	for _, stage := range []string{"precheck", "postcheck"} {
		t.Run(stage, func(t *testing.T) {
			var tags, embed atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/embed" {
					embed.Add(1)
					fmt.Fprint(w, fixtureVectors(1))
					return
				}
				n := tags.Add(1)
				digest := fixtureDigest
				if stage == "precheck" || n == 2 {
					digest = strings.Repeat("2", 64)
				}
				fmt.Fprint(w, fixtureInventory(digest))
			}))
			defer s.Close()
			p := prepareFixture(t, s.URL)
			batch, err := p.Embed(context.Background(), Document, []string{"text"})
			requireCode(t, err, ModelMismatch)
			if len(batch.Vectors) != 0 || batch.Binding.Digest != "" || stage == "precheck" && embed.Load() != 0 || stage == "postcheck" && embed.Load() != 1 {
				t.Fatal("mismatch returned a qualified batch")
			}
		})
	}
}

func TestProviderAdmissionCancellationAndJoinedClose(t *testing.T) {
	entered := make(chan struct{}, 2)
	var requests atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer s.Close()
	p := prepareFixture(t, s.URL)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := p.Embed(context.Background(), Document, []string{"text"}); results <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("admitted request did not enter")
		}
	}
	_, err := p.Embed(context.Background(), Document, []string{"text"})
	requireCode(t, err, Busy)
	if requests.Load() != 2 {
		t.Fatal("busy request performed I/O")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		requireCode(t, <-results, Canceled)
	}
	active, closed := p.inFlight.Load(), p.closed.Load()
	if active != 0 || !closed {
		t.Fatal("close did not seal and join provider lifetime")
	}
	_, err = p.Embed(context.Background(), Document, []string{"text"})
	requireCode(t, err, Closed)
	if err := p.Close(ctx); err != nil || requests.Load() != 2 {
		t.Fatal("repeated close changed state")
	}
}

func TestCallerDeadlineCoversWholeSequence(t *testing.T) {
	var requests atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-time.After(50 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		if r.URL.Path == "/api/tags" {
			fmt.Fprint(w, fixtureInventory(fixtureDigest))
		} else {
			fmt.Fprint(w, fixtureVectors(1))
		}
	}))
	defer s.Close()
	p := prepareFixture(t, s.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 125*time.Millisecond)
	defer cancel()
	batch, err := p.Embed(ctx, Document, []string{"text"})
	requireCode(t, err, TimedOut)
	if !errors.Is(err, context.DeadlineExceeded) || len(batch.Vectors) != 0 || requests.Load() > 3 {
		t.Fatal("deadline did not cover whole sequence")
	}
}

func TestCloseTimeoutSealsAdmissionAndRetainsJoin(t *testing.T) {
	p, err := Prepare(fixtureSpec("http://127.0.0.1:1"))
	if err != nil {
		t.Fatal("pure preparation failed")
	}
	p.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = p.Close(ctx)
	if !p.closed.Load() {
		p.mu.Unlock()
		t.Fatal("close timeout did not seal admission")
	}
	select {
	case <-p.done:
		p.mu.Unlock()
		t.Fatal("close falsely joined held admission")
	default:
	}
	p.mu.Unlock()
	requireCode(t, err, TimedOut)
	if err := p.Close(context.Background()); err != nil {
		t.Fatal("owned join was abandoned")
	}
	if p.inFlight.Load() != 0 || p.root.Err() == nil {
		t.Fatal("closed lifetime remained active")
	}
	_, err = p.Embed(context.Background(), Document, []string{"text"})
	requireCode(t, err, Closed)
}

func TestOwnerDeadlineIsNotResetBetweenRequests(t *testing.T) {
	var requests atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body := fixtureInventory(fixtureDigest)
		if r.URL.Path == "/api/embed" {
			body = fixtureVectors(1)
		}
		// Headers arrive promptly; each body is individually within the 10s
		// budget, but the complete three-request operation exceeds it.
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-time.After(4 * time.Second):
			fmt.Fprint(w, body)
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	p := prepareFixture(t, s.URL)
	batch, err := p.Embed(context.Background(), Document, []string{"text"})
	requireCode(t, err, TimedOut)
	if len(batch.Vectors) != 0 || requests.Load() > 3 {
		t.Fatal("owner budget reset or request retried")
	}
}

func TestNoAutomaticRetryRedirectProxyOrErrorDisclosure(t *testing.T) {
	var proxyRequests atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxyRequests.Add(1) }))
	defer proxy.Close()
	// A new literal-loopback transport must ignore both proxy environment and
	// a hostile redirect. No default transport or ambient credential is used.
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	for _, mode := range []string{"disconnect", "redirect", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch mode {
				case "disconnect":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					}
				case "redirect":
					w.Header().Set("Location", proxy.URL+"/secret?credential=synthetic")
					w.WriteHeader(302)
				case "rejected":
					w.WriteHeader(403)
					fmt.Fprint(w, "synthetic-private-provider-body")
				}
			}))
			defer s.Close()
			p := prepareFixture(t, s.URL)
			_, err := p.Embed(context.Background(), Document, []string{"synthetic-private-source"})
			code := Rejected
			if mode == "disconnect" {
				code = Unavailable
			}
			requireCode(t, err, code)
			if requests.Load() != 1 || proxyRequests.Load() != 0 {
				t.Fatal("transport retried or escaped prepared route")
			}
		})
	}
}

func TestResponseWireBoundsAndChunkedSuccess(t *testing.T) {
	cases := []struct {
		name string
		wire func(string) string
		code Code
	}{
		{"chunked", func(body string) string {
			return fmt.Sprintf("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", len(body), body)
		}, ""},
		{"informational", func(body string) string {
			return "HTTP/1.1 103 Early Hints\r\nLink: synthetic\r\n\r\n" + fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
		}, InvalidResponse},
		{"oversized-header", func(body string) string {
			return "HTTP/1.1 200 OK\r\nX-Synthetic: " + strings.Repeat("x", initialHeaderBytes) + "\r\nContent-Length: 0\r\n\r\n"
		}, Unavailable},
		{"declared-trailer", func(body string) string {
			return "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-Synthetic\r\n\r\n0\r\nX-Synthetic: value\r\n\r\n"
		}, InvalidResponse},
		{"undeclared-trailer", func(body string) string {
			return fmt.Sprintf("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\nX-Synthetic: value\r\n\r\n", len(body), body)
		}, InvalidResponse},
		{"oversized-trailer", func(body string) string {
			return fmt.Sprintf("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\nX-Synthetic: %s\r\n\r\n", len(body), body, strings.Repeat("x", trailerBufferBytes))
		}, InvalidResponse},
		{"oversized-length", func(body string) string {
			return fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", maxResponseBytes+1)
		}, ResponseTooLarge},
		{"oversized-chunked", func(body string) string {
			huge := strings.Repeat("x", maxResponseBytes+1)
			return fmt.Sprintf("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", len(huge), huge)
		}, ResponseTooLarge},
		{"compression", func(body string) string {
			return "HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\nContent-Length: 0\r\n\r\n"
		}, InvalidResponse},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var requests atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body := fixtureInventory(fixtureDigest)
				if r.URL.Path == "/api/embed" {
					body = fixtureVectors(1)
				}
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				rw.WriteString(c.wire(body))
				rw.Flush()
			}))
			defer s.Close()
			p := prepareFixture(t, s.URL)
			batch, err := p.Embed(context.Background(), Document, []string{"text"})
			if c.code == "" {
				if err != nil || len(batch.Vectors) != 1 || requests.Load() != 3 {
					t.Fatal("trailer-free chunked embedding failed")
				}
			} else {
				requireCode(t, err, c.code)
				if len(batch.Vectors) != 0 || requests.Load() != 1 {
					t.Fatal("invalid wire response continued sequence")
				}
			}
		})
	}
}

func TestLiteralIPv6PreparationDoesNotResolve(t *testing.T) {
	spec := fixtureSpec("http://[::1]:11434")
	p, err := Prepare(spec)
	if err != nil {
		t.Fatal("literal IPv6 loopback rejected")
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFreshConnectionDisconnectCannotReplayInventory(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("fixture listen failed")
	}
	defer listener.Close()
	var accepts atomic.Int64
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			reader := bufio.NewReader(conn)
			http.ReadRequest(reader)
			conn.Close()
		}
	}()
	p := prepareFixture(t, "http://"+listener.Addr().String())
	_, err = p.Embed(context.Background(), Document, []string{"text"})
	requireCode(t, err, Unavailable)
	listener.Close()
	<-joined
	if accepts.Load() != 1 {
		t.Fatal("fresh connection failure was retried")
	}
}

func TestRestrictedPackageHasNoSetupSideEffects(t *testing.T) {
	// Keep this adapter independent of the generic RAG setup/credential graph.
	for _, name := range []string{"provider.go", "decode.go", "connection.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal("source guard unavailable")
		}
		for _, forbidden := range []string{"os/exec", "os.Getenv", "go-keyring", "/internal/rag", "http.DefaultClient", "http.DefaultTransport", "net.Lookup", "/api/pull", "/api/generate", "/api/show"} {
			if strings.Contains(string(data), forbidden) {
				t.Fatal("restricted adapter gained forbidden setup or ambient routing dependency")
			}
		}
	}
}
