// Package embedding implements the explicitly prepared, restricted local-model
// transport. It does not discover, install, start, configure or own a provider.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	maxInputBytes          = 4096
	maxBatch               = 8
	maxRequestBytes        = 64 << 10
	maxResponseBytes       = 512 << 10
	initialHeaderBytes     = 4096
	trailerBufferBytes     = 4096
	callTimeout            = 10 * time.Second
	queryInstruction       = "Instruct: Retrieve documents relevant to this query.\nQuery: "
	smokeInput             = "local embedding readiness probe"
	InstructionPolicy      = "local-document-query-v1"
	TrustedFixedLocalModel = "trusted_fixed_local_model"
)

// Spec is trusted preparation input, never a browser or per-query destination.
// The operator must keep this already-running local-only model immutable for
// the owner's lifetime. Inventory checks cannot attest weights or server egress.
type Spec struct {
	Endpoint   string
	Model      string
	Digest     string
	Dimensions int
	Evidence   string
}

type Purpose string

const (
	Document Purpose = "document"
	Query    Purpose = "query"
)

// Binding reports only the qualified model/policy, not private routing data.
type Binding struct {
	Digest            string
	Dimensions        int
	InstructionPolicy string
	Evidence          string
}

type Batch struct {
	Binding Binding
	Vectors [][]float32
}

type Code string

const (
	InvalidSpec      Code = "invalid_spec"
	InvalidInput     Code = "invalid_input"
	Busy             Code = "busy"
	Closed           Code = "closed"
	Canceled         Code = "canceled"
	TimedOut         Code = "timed_out"
	Unavailable      Code = "provider_unavailable"
	Rejected         Code = "provider_rejected"
	ModelMismatch    Code = "model_mismatch"
	InvalidResponse  Code = "invalid_response"
	RequestTooLarge  Code = "request_too_large"
	ResponseTooLarge Code = "response_too_large"
)

// Error never includes addresses, source text, provider bodies or parser errors.
type Error struct{ Code Code }

func (e Error) Error() string { return "local embeddings: " + string(e.Code) }
func (e Error) Is(target error) bool {
	return e.Code == Canceled && target == context.Canceled || e.Code == TimedOut && target == context.DeadlineExceeded
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*:[A-Za-z0-9][A-Za-z0-9._-]*$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Provider's admission fence covers the entire inventory/embed/inventory call.
// Close cancels and joins those calls; it never shuts down the external server.
type Provider struct {
	spec      Spec
	client    *http.Client
	transport *http.Transport
	root      context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    atomic.Bool
	inFlight  atomic.Int32
	active    sync.WaitGroup
	done      chan struct{}
}

// Prepare is pure: no dialing, environment lookup, credential lookup or goroutine.
func Prepare(spec Spec) (*Provider, error) {
	if len(spec.Endpoint) > 128 {
		return nil, Error{InvalidSpec}
	}
	u, err := url.Parse(spec.Endpoint)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return nil, Error{InvalidSpec}
	}
	ip, err := netip.ParseAddr(u.Hostname())
	port, portErr := strconv.Atoi(u.Port())
	if err != nil || ip.Zone() != "" || !ip.IsLoopback() || portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != u.Port() ||
		len(spec.Model) > 128 || !modelPattern.MatchString(spec.Model) || strings.Contains(strings.ToLower(spec.Model), "cloud") ||
		!digestPattern.MatchString(spec.Digest) || spec.Dimensions < 1 || spec.Dimensions > 4096 || spec.Evidence != TrustedFixedLocalModel {
		return nil, Error{InvalidSpec}
	}
	address := net.JoinHostPort(ip.String(), strconv.Itoa(port))
	if u.Host != address || spec.Endpoint != "http://"+address {
		return nil, Error{InvalidSpec}
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		MaxConnsPerHost:        2,
		MaxResponseHeaderBytes: initialHeaderBytes,
		ReadBufferSize:         trailerBufferBytes,
		ResponseHeaderTimeout:  2 * time.Second,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			// Defense in depth: transport may dial only the prepared literal.
			if network != "tcp" || target != address {
				return nil, Error{InvalidSpec}
			}
			conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", address)
			if err != nil {
				return nil, err
			}
			return newRequestConnection(conn), nil
		},
	}
	root, cancel := context.WithCancel(context.Background())
	return &Provider{spec: spec, transport: transport, root: root, cancel: cancel, done: make(chan struct{}), client: &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (p *Provider) admit() error {
	if !p.mu.TryLock() {
		return Error{Busy}
	}
	defer p.mu.Unlock()
	if p.closed.Load() {
		return Error{Closed}
	}
	if p.inFlight.Load() == 2 {
		return Error{Busy}
	}
	p.inFlight.Add(1)
	p.active.Add(1) // before Close can seal admission and wait
	return nil
}

func (p *Provider) release() {
	p.inFlight.Add(-1)
	p.active.Done()
}

func (p *Provider) Close(ctx context.Context) error {
	if p.closed.CompareAndSwap(false, true) {
		p.cancel()
		go func() {
			// Seal is already visible. Serialize with an admission which
			// checked the old seal before starting Wait; never Add after Wait.
			// A caller deadline may expire, but this owned join is retained.
			p.mu.Lock()
			p.mu.Unlock()
			p.active.Wait()
			p.transport.CloseIdleConnections()
			close(p.done)
		}()
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return contextError(ctx)
	}
}

func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Error{TimedOut}
	}
	return Error{Canceled}
}

// Smoke uses a constant public probe and sends no cached source text.
func (p *Provider) Smoke(ctx context.Context) (Binding, error) {
	batch, err := p.Embed(ctx, Document, []string{smokeInput})
	return batch.Binding, err
}

func (p *Provider) Embed(ctx context.Context, purpose Purpose, inputs []string) (Batch, error) {
	if err := ctx.Err(); err != nil {
		return Batch{}, contextError(ctx)
	}
	if purpose != Document && purpose != Query || len(inputs) < 1 || len(inputs) > maxBatch {
		return Batch{}, Error{InvalidInput}
	}
	prepared := make([]string, len(inputs))
	for i, input := range inputs {
		limit := maxInputBytes
		if purpose == Query {
			limit -= len(queryInstruction)
		}
		// Bound the original bytes before scanning or copying them. Prefixing
		// a rejected arbitrary-size query must not amplify memory usage.
		if len(input) < 1 || len(input) > limit || !utf8.ValidString(input) || strings.TrimSpace(input) == "" {
			return Batch{}, Error{InvalidInput}
		}
		if purpose == Query {
			input = queryInstruction + input
		}
		prepared[i] = input
	}
	requestBytes, err := json.Marshal(struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Truncate bool     `json:"truncate"`
	}{p.spec.Model, prepared, false})
	if err != nil || len(requestBytes) > maxRequestBytes {
		return Batch{}, Error{RequestTooLarge}
	}
	if err := p.admit(); err != nil {
		return Batch{}, err
	}
	defer p.release()
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(p.root, func() {
		cancel()
		close(callbackDone)
	})
	defer func() {
		if !stop() {
			<-callbackDone // join an already admitted cancellation callback
		}
	}()
	if p.root.Err() != nil {
		cancel()
	}
	if err := p.checkModel(callCtx); err != nil {
		return Batch{}, err
	}
	body, err := p.request(callCtx, http.MethodPost, "/api/embed", requestBytes)
	if err != nil {
		return Batch{}, err
	}
	vectors, err := decodeEmbedding(body, p.spec.Model, len(inputs), p.spec.Dimensions)
	if err != nil {
		return Batch{}, err
	}
	if err := p.checkModel(callCtx); err != nil {
		return Batch{}, err
	}
	if callCtx.Err() != nil {
		return Batch{}, contextError(callCtx)
	}
	return Batch{Binding: Binding{p.spec.Digest, p.spec.Dimensions, InstructionPolicy, TrustedFixedLocalModel}, Vectors: vectors}, nil
}

func (p *Provider) checkModel(ctx context.Context) error {
	body, err := p.request(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return err
	}
	return decodeInventory(body, p.spec.Model, p.spec.Digest)
}

func (p *Provider) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, contextError(ctx)
	}
	// No request includes Expect. Refuse even a single unsolicited 1xx rather
	// than resetting the transport's header budget after each informational block.
	ctx = httptrace.WithClientTrace(requestContext{ctx}, &httptrace.ClientTrace{Got1xxResponse: func(int, textproto.MIMEHeader) error { return Error{InvalidResponse} }})
	req, err := http.NewRequestWithContext(ctx, method, p.spec.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, Error{InvalidSpec}
	}
	req.Close = true
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, contextError(ctx)
		}
		var finite Error
		if errors.As(err, &finite) {
			return nil, finite
		}
		return nil, Error{Unavailable}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, Error{Rejected}
	}
	if resp.ProtoMajor != 1 || len(resp.Trailer) != 0 || resp.Header.Get("Content-Encoding") != "" {
		return nil, Error{InvalidResponse}
	}
	if resp.ContentLength > maxResponseBytes {
		return nil, Error{ResponseTooLarge}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, contextError(ctx)
		}
		return nil, Error{InvalidResponse}
	}
	// Go's HTTP/1 trailer parser bounds undeclared trailers to ReadBufferSize.
	// Together with the initial 4 KiB ceiling this reserves at most 8 KiB for
	// headers. Refuse trailers, including valid bounded undeclared ones.
	if len(resp.Trailer) != 0 {
		return nil, Error{InvalidResponse}
	}
	if len(data) > maxResponseBytes {
		return nil, Error{ResponseTooLarge}
	}
	if ctx.Err() != nil {
		return nil, contextError(ctx)
	}
	return data, nil
}
