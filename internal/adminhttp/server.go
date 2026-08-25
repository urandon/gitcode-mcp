package adminhttp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "gitcode_mcp_admin_session"

type Readiness struct {
	APIVersion     string    `json:"api_version"`
	Version        string    `json:"version"`
	DaemonRunning  bool      `json:"daemon_running"`
	SessionSecure  bool      `json:"session_secure"`
	CacheConnected bool      `json:"cache_connected"`
	CacheReference string    `json:"cache_reference,omitempty"`
	SchemaVersion  int       `json:"schema_version,omitempty"`
	CheckedAt      time.Time `json:"checked_at"`
}

type Status struct {
	Running   bool      `json:"running"`
	URL       string    `json:"url,omitempty"`
	Bind      string    `json:"bind,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

type OpenResult struct {
	Status Status `json:"status"`
	URL    string `json:"url"`
}

type OpenRequest struct {
	LaunchTokenHash string `json:"launch_token_hash"`
}

type Config struct {
	Bind              string
	AllowNonLoopback  bool
	SessionTTL        time.Duration
	Assets            fs.FS
	Readiness         func(context.Context) Readiness
	Snapshot          SnapshotProvider
	EventCapacity     int
	EventPollInterval time.Duration
	CancelJob         JobActionProvider
	RetryJob          JobActionProvider
}

type Controller struct {
	cfg      Config
	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	status   Status
	launch   map[[32]byte]time.Time
	sessions map[[32]byte]session
	events   *eventLog
}

type session struct {
	Expires time.Time
	CSRF    string
}

func New(cfg Config) *Controller {
	if strings.TrimSpace(cfg.Bind) == "" {
		cfg.Bind = "127.0.0.1:0"
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 8 * time.Hour
	}
	if cfg.EventPollInterval <= 0 {
		cfg.EventPollInterval = time.Second
	}
	return &Controller{cfg: cfg, launch: map[[32]byte]time.Time{}, sessions: map[[32]byte]session{}, events: newEventLog(cfg.EventCapacity)}
}

func (c *Controller) Start(ctx context.Context) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listener != nil {
		return c.status, nil
	}
	if err := validateBind(c.cfg.Bind, c.cfg.AllowNonLoopback); err != nil {
		return Status{}, err
	}
	ln, err := net.Listen("tcp", c.cfg.Bind)
	if err != nil {
		return Status{}, fmt.Errorf("admin: listen: %w", err)
	}
	baseURL := "http://" + ln.Addr().String()
	c.listener = ln
	c.status = Status{Running: true, URL: baseURL, Bind: ln.Addr().String(), StartedAt: time.Now().UTC()}
	c.server = &http.Server{Handler: c.handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.server.Shutdown(shutdownCtx)
	}()
	go func() { _ = c.server.Serve(ln) }()
	if c.cfg.Snapshot != nil {
		go c.pollObservation(ctx)
	}
	return c.status, nil
}

func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Controller) Open(ctx context.Context, request OpenRequest) (OpenResult, error) {
	status, err := c.Start(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(request.LaunchTokenHash))
	if err != nil || len(decoded) != sha256.Size {
		return OpenResult{}, errors.New("admin: invalid launch token hash")
	}
	var hash [sha256.Size]byte
	copy(hash[:], decoded)
	c.mu.Lock()
	c.pruneLocked(time.Now())
	c.launch[hash] = time.Now().Add(time.Minute)
	c.mu.Unlock()
	return OpenResult{Status: status, URL: status.URL}, nil
}

func NewLaunchToken() (token string, encodedHash string, err error) {
	token, err = randomToken()
	if err != nil {
		return "", "", err
	}
	hash := sha256.Sum256([]byte(token))
	return token, base64.RawURLEncoding.EncodeToString(hash[:]), nil
}

func (c *Controller) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/v1/session", c.exchangeSession)
	mux.HandleFunc("GET /api/admin/v1/session", c.getSession)
	mux.HandleFunc("DELETE /api/admin/v1/session", c.deleteSession)
	mux.HandleFunc("GET /api/admin/v1/readiness", c.getReadiness)
	mux.HandleFunc("GET /api/admin/v1/snapshot", c.getSnapshot)
	mux.HandleFunc("GET /api/admin/v1/caches", c.getCaches)
	mux.HandleFunc("GET /api/admin/v1/caches/{cache_ref}", c.getCache)
	mux.HandleFunc("GET /api/admin/v1/caches/{cache_ref}/repositories/{repo_id...}", c.getRepository)
	mux.HandleFunc("GET /api/admin/v1/jobs", c.getJobs)
	mux.HandleFunc("GET /api/admin/v1/jobs/{job_id}", c.getJob)
	mux.HandleFunc("POST /api/admin/v1/jobs/{job_id}/cancel", c.cancelJob)
	mux.HandleFunc("POST /api/admin/v1/jobs/{job_id}/retry", c.retryJob)
	mux.HandleFunc("GET /api/admin/v1/maintenance", c.getMaintenance)
	mux.HandleFunc("GET /api/admin/v1/diagnostics", c.getDiagnostics)
	mux.HandleFunc("GET /api/admin/v1/capabilities", c.getCapabilities)
	mux.HandleFunc("GET /api/admin/v1/events", c.getEvents)
	mux.HandleFunc("/", c.serveAsset)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		if !c.validHost(r.Host) {
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (c *Controller) pollObservation(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.EventPollInterval)
	defer ticker.Stop()
	var previous ObservationSnapshot
	for {
		snapshot, err := c.snapshot(ctx)
		if err == nil {
			if previous.Revision == "" {
				c.events.append(Event{Kind: "snapshot_changed", EntityType: "snapshot", EntityID: "overview", Revision: snapshot.Revision})
			} else if snapshot.Revision != previous.Revision {
				c.publishObservationDiff(previous, snapshot)
			}
			previous = snapshot
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Controller) publishObservationDiff(previous, current ObservationSnapshot) {
	previousJobs := make(map[string]JobObservation, len(previous.Jobs))
	for _, job := range previous.Jobs {
		previousJobs[job.ID] = job
	}
	for _, job := range current.Jobs {
		if before, ok := previousJobs[job.ID]; !ok || before.Status != job.Status || !before.UpdatedAt.Equal(job.UpdatedAt) || before.Completed != job.Completed {
			c.events.append(Event{Kind: "job_changed", EntityType: "job", EntityID: job.ID, Revision: current.Revision})
		}
	}
	previousCaches := make(map[string]CacheObservation, len(previous.Caches))
	for _, cache := range previous.Caches {
		previousCaches[cache.CacheRef] = cache
	}
	for _, cache := range current.Caches {
		before, ok := previousCaches[cache.CacheRef]
		if !ok || before.Readiness != cache.Readiness || before.RecordCount != cache.RecordCount || before.ChunkCount != cache.ChunkCount || before.RepositoryCount != cache.RepositoryCount {
			c.events.append(Event{Kind: "cache_changed", EntityType: "cache", EntityID: cache.CacheRef, Revision: current.Revision})
		}
	}
	previousMaintenance := make(map[string]MaintenanceObservation, len(previous.Maintenance))
	for _, entry := range previous.Maintenance {
		previousMaintenance[entry.RegistrationID] = entry
	}
	for _, entry := range current.Maintenance {
		before, ok := previousMaintenance[entry.RegistrationID]
		if !ok || before.Generation != entry.Generation || before.State != entry.State || before.Enabled != entry.Enabled {
			c.events.append(Event{Kind: "maintenance_changed", EntityType: "maintenance", EntityID: entry.RegistrationID, Revision: current.Revision})
		}
	}
	c.events.append(Event{Kind: "snapshot_changed", EntityType: "snapshot", EntityID: "overview", Revision: current.Revision})
}

func (c *Controller) exchangeSession(w http.ResponseWriter, r *http.Request) {
	if !c.validMutationOrigin(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	var req struct {
		LaunchToken string `json:"launch_token"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.LaunchToken) == "" {
		http.Error(w, "invalid launch token", http.StatusBadRequest)
		return
	}
	now := time.Now()
	hash := sha256.Sum256([]byte(req.LaunchToken))
	c.mu.Lock()
	c.pruneLocked(now)
	expires, ok := c.launch[hash]
	if ok {
		delete(c.launch, hash)
	}
	c.mu.Unlock()
	if !ok || now.After(expires) {
		http.Error(w, "invalid launch token", http.StatusUnauthorized)
		return
	}
	rawSession, err := randomToken()
	if err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	csrf, err := randomToken()
	if err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	c.mu.Lock()
	c.sessions[sha256.Sum256([]byte(rawSession))] = session{Expires: now.Add(c.cfg.SessionTTL), CSRF: csrf}
	c.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: rawSession, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(c.cfg.SessionTTL.Seconds())})
	writeJSON(w, http.StatusCreated, map[string]string{"csrf_token": csrf})
}

func (c *Controller) getReadiness(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.authenticate(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	value := Readiness{APIVersion: "1", DaemonRunning: true, SessionSecure: true, CheckedAt: time.Now().UTC()}
	if c.cfg.Readiness != nil {
		value = c.cfg.Readiness(r.Context())
		value.DaemonRunning = true
		value.SessionSecure = true
		value.APIVersion = "1"
		value.CheckedAt = time.Now().UTC()
	}
	writeJSON(w, http.StatusOK, value)
}

func (c *Controller) deleteSession(w http.ResponseWriter, r *http.Request) {
	s, ok := c.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !c.validMutationOrigin(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF)) != 1 {
		http.Error(w, "csrf validation failed", http.StatusForbidden)
		return
	}
	cookie, _ := r.Cookie(sessionCookie)
	if cookie != nil {
		c.mu.Lock()
		delete(c.sessions, sha256.Sum256([]byte(cookie.Value)))
		c.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (c *Controller) authenticate(r *http.Request) (session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.pruneLocked(now)
	s, ok := c.sessions[sha256.Sum256([]byte(cookie.Value))]
	return s, ok && now.Before(s.Expires)
}

func (c *Controller) serveAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(c.cfg.Assets, name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) || strings.Contains(path.Base(name), ".") {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
		data, err = fs.ReadFile(c.cfg.Assets, name)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.Contains(name, "/immutable/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	}
	w.Header().Set("Content-Type", contentType(name))
	_, _ = w.Write(data)
}

func (c *Controller) validHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if c.cfg.AllowNonLoopback {
		return strings.TrimSpace(host) != ""
	}
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

func (c *Controller) validMutationOrigin(r *http.Request) bool {
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host) && u.Scheme == "http"
}

func (c *Controller) pruneLocked(now time.Time) {
	for key, expires := range c.launch {
		if !now.Before(expires) {
			delete(c.launch, key)
		}
	}
	for key, value := range c.sessions {
		if !now.Before(value.Expires) {
			delete(c.sessions, key)
		}
	}
}

func validateBind(bind string, unsafe bool) error {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Errorf("admin: invalid bind address %q: %w", bind, err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if !unsafe && !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("admin: non-loopback bind %q requires --admin-unsafe-allow-non-loopback", bind)
	}
	return nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func setSecurityHeaders(w http.ResponseWriter) {
	// The prerendered SvelteKit index carries the generated hash policy for its
	// inline bootstrap. frame-ancestors is ineffective in a meta policy, so it
	// remains an independent response header here.
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}
