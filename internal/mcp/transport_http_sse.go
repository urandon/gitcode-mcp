package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ReadinessProbe func(context.Context) Readiness

type Readiness struct {
	Ready     bool       `json:"ready"`
	Code      string     `json:"code,omitempty"`
	Message   string     `json:"message,omitempty"`
	ErrorData *errorData `json:"error_data,omitempty"`
}

type ServerConfig struct {
	BindAddress     string
	ReadinessProbe  ReadinessProbe
	Logger          *log.Logger
	RequestID       func() string
	SessionID       func() string
	PerSessionQueue int
}

type HTTPSSETransport struct {
	handler  *RPCHandler
	config   ServerConfig
	registry *sessionRegistry
}

type ClientSession struct {
	id           string
	ctx          context.Context
	cancel       context.CancelFunc
	out          chan response
	lastActivity time.Time
	mu           sync.Mutex
	closed       bool
}

type sessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*ClientSession
}

type transportError struct {
	Error errorData `json:"error"`
}

func NewHTTPSSETransport(handler *RPCHandler, config ServerConfig) *HTTPSSETransport {
	if config.BindAddress == "" {
		config.BindAddress = "127.0.0.1:0"
	}
	if config.Logger == nil {
		config.Logger = log.New(io.Discard, "", 0)
	}
	if config.RequestID == nil {
		config.RequestID = randomID
	}
	if config.SessionID == nil {
		config.SessionID = randomID
	}
	if config.PerSessionQueue <= 0 {
		config.PerSessionQueue = 16
	}
	if config.ReadinessProbe == nil {
		config.ReadinessProbe = func(context.Context) Readiness { return Readiness{Ready: true} }
	}
	return &HTTPSSETransport{handler: handler, config: config, registry: &sessionRegistry{sessions: map[string]*ClientSession{}}}
}

func (t *HTTPSSETransport) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", t.health)
	mux.HandleFunc("/ready", t.ready)
	mux.HandleFunc("/sse", t.sse)
	mux.HandleFunc("/message", t.message)
	return mux
}

func (t *HTTPSSETransport) Serve(ctx context.Context) error {
	server := &http.Server{Addr: t.config.BindAddress, Handler: t.Handler()}
	ln, err := net.Listen("tcp", t.config.BindAddress)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- server.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		err := <-errc
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case err := <-errc:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (t *HTTPSSETransport) health(w http.ResponseWriter, r *http.Request) {
	reqID := t.requestID(w, r)
	if r.Method != http.MethodGet {
		t.writeTransportError(w, reqID, "/health", "", http.StatusMethodNotAllowed, "method_not_allowed", "GET is required")
		return
	}
	t.config.Logger.Printf("request_id=%s route=/health status=200", reqID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (t *HTTPSSETransport) ready(w http.ResponseWriter, r *http.Request) {
	reqID := t.requestID(w, r)
	if r.Method != http.MethodGet {
		t.writeTransportError(w, reqID, "/ready", "", http.StatusMethodNotAllowed, "method_not_allowed", "GET is required")
		return
	}
	ready := t.config.ReadinessProbe(r.Context())
	if ready.Ready {
		t.config.Logger.Printf("request_id=%s route=/ready status=200", reqID)
		writeJSON(w, http.StatusOK, ready)
		return
	}
	t.config.Logger.Printf("request_id=%s route=/ready status=503 code=%s", reqID, ready.Code)
	writeJSON(w, http.StatusServiceUnavailable, ready)
}

func (t *HTTPSSETransport) sse(w http.ResponseWriter, r *http.Request) {
	reqID := t.requestID(w, r)
	if r.Method != http.MethodGet {
		t.writeTransportError(w, reqID, "/sse", "", http.StatusMethodNotAllowed, "method_not_allowed", "GET is required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.writeTransportError(w, reqID, "/sse", "", http.StatusInternalServerError, "sse_unsupported", "streaming is not supported")
		return
	}
	session := t.registry.create(r.Context(), t.config.SessionID(), t.config.PerSessionQueue)
	defer t.registry.close(session.id)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	endpoint := "/message?session_id=" + session.id
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint)
	flusher.Flush()
	t.config.Logger.Printf("request_id=%s route=/sse session_id=%s status=connected", reqID, session.id)
	for {
		select {
		case <-session.ctx.Done():
			t.config.Logger.Printf("request_id=%s route=/sse session_id=%s status=closed", reqID, session.id)
			return
		case resp, ok := <-session.out:
			if !ok {
				return
			}
			b, _ := json.Marshal(resp)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(b))
			flusher.Flush()
		}
	}
}

func (t *HTTPSSETransport) message(w http.ResponseWriter, r *http.Request) {
	reqID := t.requestID(w, r)
	if r.Method != http.MethodPost {
		t.writeTransportError(w, reqID, "/message", "", http.StatusMethodNotAllowed, "method_not_allowed", "POST is required")
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		t.writeTransportError(w, reqID, "/message", "", http.StatusBadRequest, "missing_session", "session_id is required")
		return
	}
	session, ok := t.registry.get(sessionID)
	if !ok {
		t.writeTransportError(w, reqID, "/message", sessionID, http.StatusNotFound, "unknown_session", "session is not live")
		return
	}
	defer r.Body.Close()
	var req request
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		t.writeTransportError(w, reqID, "/message", sessionID, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := ensureSingleJSONValue(dec); err != nil {
		t.writeTransportError(w, reqID, "/message", sessionID, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	ctx, cancel := context.WithCancel(session.ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		select {
		case <-r.Context().Done():
			cancel()
		case <-done:
		}
	}()
	defer close(done)
	resp, emit := t.handler.Handle(ctx, req)
	if ctx.Err() != nil || r.Context().Err() != nil || session.isClosed() {
		t.config.Logger.Printf("request_id=%s route=/message session_id=%s status=cancelled", reqID, sessionID)
		return
	}
	if emit && resp != nil {
		if ok := session.enqueue(ctx, *resp); !ok {
			if ctx.Err() != nil {
				t.config.Logger.Printf("request_id=%s route=/message session_id=%s status=cancelled", reqID, sessionID)
				return
			}
			t.writeTransportError(w, reqID, "/message", sessionID, http.StatusTooManyRequests, "session_queue_full", "session response queue is full")
			return
		}
	}
	t.registry.touch(sessionID)
	w.WriteHeader(http.StatusAccepted)
	t.config.Logger.Printf("request_id=%s route=/message session_id=%s status=202", reqID, sessionID)
}

func (t *HTTPSSETransport) requestID(w http.ResponseWriter, r *http.Request) string {
	reqID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if reqID == "" {
		reqID = t.config.RequestID()
	}
	w.Header().Set("X-Request-ID", reqID)
	return reqID
}

func (t *HTTPSSETransport) writeTransportError(w http.ResponseWriter, reqID string, route string, sessionID string, status int, code string, message string) {
	if sessionID == "" {
		t.config.Logger.Printf("request_id=%s route=%s status=%d code=%s", reqID, route, status, code)
	} else {
		t.config.Logger.Printf("request_id=%s route=%s session_id=%s status=%d code=%s", reqID, route, sessionID, status, code)
	}
	writeJSON(w, status, transportError{Error: errorData{Code: code, Message: message}})
}

func (r *sessionRegistry) create(parent context.Context, id string, queue int) *ClientSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	baseID := strings.TrimSpace(id)
	if baseID == "" {
		baseID = randomID()
	}
	uniqueID := baseID
	for i := 2; ; i++ {
		if _, exists := r.sessions[uniqueID]; !exists {
			break
		}
		uniqueID = fmt.Sprintf("%s-%d", baseID, i)
	}
	ctx, cancel := context.WithCancel(parent)
	s := &ClientSession{id: uniqueID, ctx: ctx, cancel: cancel, out: make(chan response, queue), lastActivity: time.Now().UTC()}
	r.sessions[uniqueID] = s
	return s
}

func (r *sessionRegistry) get(id string) (*ClientSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok || s.isClosed() {
		return nil, false
	}
	return s, true
}

func (r *sessionRegistry) touch(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[id]; ok {
		s.lastActivity = time.Now().UTC()
	}
}

func (r *sessionRegistry) close(id string) {
	r.mu.Lock()
	s, ok := r.sessions[id]
	if ok {
		delete(r.sessions, id)
	}
	r.mu.Unlock()
	if ok {
		s.close()
	}
}

func (s *ClientSession) enqueue(ctx context.Context, resp response) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	select {
	case <-ctx.Done():
		s.mu.Unlock()
		return false
	case s.out <- resp:
		s.mu.Unlock()
		return true
	default:
		s.mu.Unlock()
		return false
	}
}

func (s *ClientSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cancel()
	close(s.out)
	s.mu.Unlock()
}

func (s *ClientSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func ensureSingleJSONValue(dec *json.Decoder) error {
	var extra json.RawMessage
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("exactly one JSON-RPC request is required")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
