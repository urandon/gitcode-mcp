package servicectl

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/adminui"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/observability"
)

// Isolate broad Admin snapshot reads as well as execution. No cwd discovery,
// machine credentials, installed service state or embedding provider is used.
type navigationSource struct{ root string }

func (s navigationSource) Env(k string) string {
	if k == config.EnvToken {
		return "synthetic-private-credential"
	}
	return ""
}
func (s navigationSource) UserHomeDir() (string, error)    { return s.root, nil }
func (s navigationSource) UserConfigDir() (string, error)  { return s.root, nil }
func (s navigationSource) UserCacheDir() (string, error)   { return s.root, nil }
func (s navigationSource) WorkingDir() (string, error)     { return s.root, nil }
func (s navigationSource) ReadFile(string) ([]byte, error) { return nil, os.ErrNotExist }

func TestO5ActualAdminEntityAndSourceNavigation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	// Git fixture setup and read-only inspection must not inherit signing,
	// hooks or other settings from machine-global configuration.
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v5/repos/owner/repo/issues" {
			t.Error("unexpected navigation fixture provider operation")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"synthetic-private-provider-body"}`)
	}))
	defer provider.Close()
	path := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal("navigation cache unavailable")
	}
	defer store.Close()
	if store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: provider.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}) != nil {
		t.Fatal("navigation binding unavailable")
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal("navigation identity unavailable")
	}
	cfg := adminFakeRAGConfig()
	cfg.CachePath, cfg.LockPath, cfg.GitCodeBaseURL = path, path+".lock", provider.URL
	cfg.MaxRetries, cfg.DefaultTimeout = 0, time.Second
	p := cfg.RAG.Providers["fake"]
	p.DataBoundary = "unknown"
	cfg.RAG.Providers["fake"] = p
	manager := Manager{Source: navigationSource{root}, RuntimeDir: filepath.Join(root, "runtime"), AdminCachePath: path, EffectiveConfig: &cfg, Version: "test-version"}
	jobs := NewJobManager(filepath.Join(root, "jobs.json"))
	c := maintenanceEventCollector(t)
	jobs.SetObservation(c)
	metrics := observability.NewMetrics(c.BootID(), time.Now().UTC())
	jobs.SetMetrics(metrics)
	m := NewMaintenanceManager(manager, jobs, filepath.Join(root, "registry.json"))
	enroll := testMaintenanceEnrollRequest(path, "navigation-enroll", MaintenancePolicy{SyncMode: "off"})
	enroll.ConfigSnapshot, enroll.ConfigHash = cfg, maintenanceHash(cfg)
	entry, err := m.Enroll(ctx, enroll)
	if err != nil {
		t.Fatal("navigation enrollment unavailable")
	}
	paths := []string{
		createRepositoryDocsRegistrationRepo(t, filepath.Join(root, "source-a"), "synthetic-private-source-a"),
		createRepositoryDocsRegistrationRepo(t, filepath.Join(root, "source-b"), "synthetic-private-source-b"),
	}
	ids := make([]string, 0, 2)
	for _, repo := range paths {
		registered, ok, err := m.RegisterRepositoryDocsSource(ctx, identity.UUID, entry.RepoID, repo, "")
		if err != nil || !ok || registered.RepositoryDocs == nil {
			t.Fatal("navigation source registration unavailable")
		}
		ids = append(ids, registered.RepositoryDocs.SourceRegistrationID)
	}
	if ids[0] == ids[1] {
		t.Fatal("distinct Git authorities collided")
	}
	job, err := jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: entry.RepoID, CachePath: path, CacheUUID: identity.UUID, RegistrationID: entry.RegistrationID, Issues: true, MaxPages: 1, MaxRecords: 100, IdempotencyKey: "navigation-sync"})
	if err != nil {
		t.Fatal("actual navigation job admission unavailable")
	}
	defer func() {
		cancel()
		deadline := time.Now().Add(5 * time.Second)
		for {
			release, active := jobs.BeginCacheMutationFence(identity.UUID)
			release()
			if len(active) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Error("navigation worker did not join")
				break
			}
			time.Sleep(time.Millisecond)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, ok := jobs.Get(job.ID)
		release, active := jobs.BeginCacheMutationFence(identity.UUID)
		release()
		if ok && current.FinishedAt != nil && len(active) == 0 {
			if current.Status != JobStatusFailed || calls.Load() != 1 {
				t.Fatal("actual failed job not conserved")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("navigation job terminal boundary unavailable")
		}
		time.Sleep(time.Millisecond)
	}
	// Real Git inspection failures establish two independent source incidents.
	for i, repo := range paths {
		if os.Rename(repo, repo+"-parked") != nil {
			t.Fatal("source failure fixture unavailable")
		}
		snapshot := cloneMaintenanceEntryPrivate(m.entries[entry.RegistrationID])
		source := m.sources[entry.RegistrationID][ids[i]]
		state := source.State
		snapshot.RepositoryDocs = &state
		snapshot.repositoryPath, snapshot.repositoryProfile, snapshot.repositorySourceID = source.RepositoryPath, source.Profile, source.State.SourceRegistrationID
		observed, flush := beginMaintenanceObservation(ctx, jobs)
		at := time.Now().UTC()
		m.reconcileRepositoryDocsState(observed, store, &snapshot, at)
		m.finishReconcileEntry(observed, entry.RegistrationID, snapshot, 0, 0, "", "", nil, Job{}, Job{}, Job{}, at)
		flush()
	}
	sealJobObservation(t, c)
	reader := &observability.Reader{Collector: c, Metrics: metrics, Health: m.ExecutionHealthContext}
	page, err := reader.Events(ctx, observability.EventsRequest{Limit: 200})
	if err != nil || page.HasMore {
		t.Fatal("bounded navigation events unavailable")
	}
	seen := map[string]bool{}
	jobTerminal, enrollment := false, false
	for _, event := range page.Events {
		if event.Code == observability.JobTerminal && event.JobRef == expectedObservationRef("job", job.ID) && event.Outcome == "failed" {
			jobTerminal = true
		}
		if event.Code == observability.MaintenanceStateChanged && event.SourceRef == "" && event.Outcome == "enrolled" {
			enrollment = true
		}
		if event.Code == observability.MaintenanceStateChanged && event.SourceRef != "" && event.Outcome == "degraded" {
			seen[event.SourceRef] = true
		}
	}
	if !jobTerminal || !enrollment || !seen[expectedObservationRef("source", ids[0])] || !seen[expectedObservationRef("source", ids[1])] {
		t.Fatal("actual causal navigation evidence missing")
	}
	read := func(ctx context.Context) (adminhttp.ObservationSnapshot, error) {
		return manager.adminObservation(ctx, jobs, m, job.CreatedAt)
	}
	snapshot, err := read(ctx)
	if err != nil || len(snapshot.Caches) != 1 || len(snapshot.Jobs) != 1 || len(snapshot.Maintenance) != 1 || len(snapshot.Caches[0].Repositories) != 1 {
		t.Fatal("ordinary Admin entity authority unavailable")
	}
	docs := snapshot.Caches[0].Repositories[0].Documentation
	if docs == nil || len(docs.Sources) != 2 {
		t.Fatal("ordinary Admin two-source authority unavailable")
	}
	for _, source := range docs.Sources {
		if source.ObservationRefs == nil || !seen[source.ObservationRefs.Source] {
			t.Fatal("source bridge lost causal identity")
		}
		if source.ObservationRefs.Cache != expectedObservationRef("cache", identity.UUID) || source.ObservationRefs.Repo != expectedObservationRef("repo", entry.RepoID) || source.ObservationRefs.Registration != expectedObservationRef("registration", entry.RegistrationID) {
			t.Fatal("source bridge omitted required exact parent authority")
		}
	}
	data, _ := json.Marshal(snapshot)
	for _, private := range []string{root, provider.URL, "synthetic-private", path, identity.UUID} {
		if strings.Contains(string(data), private) {
			t.Fatal("ordinary Admin snapshot exposed private fixture coordinates")
		}
	}
	if os.Getenv("GITCODE_MCP_O5_NAVIGATION_BROWSER") == "1" {
		assets, err := fs.Sub(adminui.Files, "assets")
		if err != nil {
			t.Fatal("embedded navigation assets unavailable")
		}
		admin := adminhttp.New(adminhttp.Config{Assets: assets, Snapshot: read, Observability: reader, EventPollInterval: 50 * time.Millisecond})
		digest := sha256.Sum256([]byte("qualification-launch"))
		opened, err := admin.Open(ctx, adminhttp.OpenRequest{LaunchTokenHash: base64.RawURLEncoding.EncodeToString(digest[:])})
		if err != nil {
			t.Fatal("authenticated navigation server unavailable")
		}
		defer func() {
			cancel()
			stop, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			if !admin.WaitStopped(stop) {
				t.Error("navigation Admin did not join")
			}
		}()
		sourceRefs := make([]map[string]string, 0, 2)
		for _, id := range ids {
			sourceRefs = append(sourceRefs, map[string]string{"id": id, "ref": expectedObservationRef("source", id)})
		}
		public, _ := json.Marshal(map[string]any{"url": opened.URL, "cache": snapshot.Caches[0].CacheRef, "repo": entry.RepoID, "job": job.ID, "registration": entry.RegistrationID, "sources": sourceRefs})
		fmt.Printf("O5_NAVIGATION %s\n", public)
		awaitNavigationCommand(t, "remove")
		// Explicit test-only removal of one real durable registry authority.
		// Never fabricate a reduced snapshot or add a public removal endpoint.
		m.mu.Lock()
		delete(m.sources[entry.RegistrationID], ids[0])
		m.refreshLegacyRepositoryDocsLocked(m.entries[entry.RegistrationID])
		err = m.saveLocked()
		m.mu.Unlock()
		if err != nil {
			t.Fatal("source removal authority not persisted")
		}
		fmt.Println("O5_NAVIGATION_REMOVED")
		awaitNavigationCommand(t, "continue")
	}
	reloaded := NewMaintenanceManager(manager, jobs, m.path)
	if reloaded.Load() != nil {
		t.Fatal("navigation registry reload unavailable")
	}
	want := 2
	if os.Getenv("GITCODE_MCP_O5_NAVIGATION_BROWSER") == "1" {
		want = 1
	}
	if len(reloaded.sources[entry.RegistrationID]) != want || len(jobs.List()) != 1 || calls.Load() != 1 {
		t.Fatal("navigation changed execution or durable source authority")
	}
	if reloaded.sources[entry.RegistrationID][ids[1]] == nil || (reloaded.sources[entry.RegistrationID][ids[0]] == nil) != (want == 1) {
		t.Fatal("navigation retained or removed the wrong exact source")
	}
}

func awaitNavigationCommand(t *testing.T, expected string) {
	t.Helper()
	result := make(chan bool, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		line, err := bufio.NewReader(io.LimitReader(os.Stdin, 64)).ReadString('\n')
		result <- err == nil && line == expected+"\n"
	}()
	select {
	case ok := <-result:
		<-joined
		if !ok {
			t.Fatal("navigation fixture protocol rejected")
		}
	case <-time.After(30 * time.Second):
		_ = os.Stdin.Close()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("navigation protocol reader did not join")
		}
		t.Fatal("navigation fixture continuation unavailable")
	}
}
