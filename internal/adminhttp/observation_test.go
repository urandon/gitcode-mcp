package adminhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestObservationSnapshotContractAndResourceBoundaries(t *testing.T) {
	fixed := time.Date(2026, 8, 25, 10, 30, 0, 0, time.UTC)
	provider := func(context.Context) (ObservationSnapshot, error) {
		return FinalizeSnapshot(ObservationSnapshot{
			Service: ServiceObservation{Version: "v2.0.0", Protocol: "admin.v1", Running: true, AdminSecure: true},
			Caches: []CacheObservation{
				{CacheRef: "cache-b", Readiness: "ready"},
				{CacheRef: "cache-a", Readiness: "ready", Repositories: []RepositoryObservation{{RepoID: "owner/repo", BindingState: "bound"}}},
			},
			Jobs: []JobObservation{{ID: "job-000002", Type: "rag", Status: "running", CreatedAt: fixed, UpdatedAt: fixed}, {ID: "job-000001", Type: "sync", Status: "succeeded", CreatedAt: fixed, UpdatedAt: fixed}},
		}, fixed), nil
	}
	c := New(Config{Assets: fstest.MapFS{"index.html": {Data: []byte("index")}}, Snapshot: provider})
	cookie := authorizeObservationController(c, fixed.Add(time.Hour))
	handler := c.handler()

	snapshotResponse := authorizedRequest(t, handler, cookie, "/api/admin/v1/snapshot")
	if snapshotResponse.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", snapshotResponse.Code, snapshotResponse.Body.String())
	}
	if snapshotResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("snapshot cache policy=%q", snapshotResponse.Header().Get("Cache-Control"))
	}
	var snapshot ObservationSnapshot
	if err := json.Unmarshal(snapshotResponse.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.APIVersion != "1" || !strings.HasPrefix(snapshot.Revision, "snapshot-") {
		t.Fatalf("versioned snapshot=%+v", snapshot)
	}
	if snapshot.Caches[0].CacheRef != "cache-a" || snapshot.Jobs[0].ID != "job-000001" {
		t.Fatalf("snapshot ordering caches=%+v jobs=%+v", snapshot.Caches, snapshot.Jobs)
	}

	cacheResponse := authorizedRequest(t, handler, cookie, "/api/admin/v1/caches/cache-a")
	if cacheResponse.Code != http.StatusOK || !strings.Contains(cacheResponse.Body.String(), `"api_version":"1"`) || !strings.Contains(cacheResponse.Body.String(), `"cache":{"cache_ref":"cache-a"`) {
		t.Fatalf("cache status=%d body=%s", cacheResponse.Code, cacheResponse.Body.String())
	}
	repoResponse := authorizedRequest(t, handler, cookie, "/api/admin/v1/caches/cache-a/repositories/owner/repo")
	if repoResponse.Code != http.StatusOK || !strings.Contains(repoResponse.Body.String(), `"repository":{"repo_id":"owner/repo"`) {
		t.Fatalf("repo status=%d body=%s", repoResponse.Code, repoResponse.Body.String())
	}
	missing := authorizedRequest(t, handler, cookie, "/api/admin/v1/caches/private-path")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"cache_not_found"`) {
		t.Fatalf("missing cache status=%d body=%s", missing.Code, missing.Body.String())
	}

	jobs := authorizedRequest(t, handler, cookie, "/api/admin/v1/jobs?limit=1&type=sync")
	if jobs.Code != http.StatusOK || !strings.Contains(jobs.Body.String(), `"job-000001"`) || strings.Contains(jobs.Body.String(), `"job-000002"`) {
		t.Fatalf("filtered jobs status=%d body=%s", jobs.Code, jobs.Body.String())
	}
	job := authorizedRequest(t, handler, cookie, "/api/admin/v1/jobs/job-000001")
	if job.Code != http.StatusOK || !strings.Contains(job.Body.String(), `"api_version":"1"`) || !strings.Contains(job.Body.String(), `"job":{"id":"job-000001"`) {
		t.Fatalf("job detail status=%d body=%s", job.Code, job.Body.String())
	}
	invalidLimit := authorizedRequest(t, handler, cookie, "/api/admin/v1/jobs?limit=1000")
	if invalidLimit.Code != http.StatusBadRequest || !strings.Contains(invalidLimit.Body.String(), `"code":"invalid_query"`) {
		t.Fatalf("invalid limit status=%d body=%s", invalidLimit.Code, invalidLimit.Body.String())
	}
	for _, endpoint := range []string{"/api/admin/v1/caches", "/api/admin/v1/maintenance", "/api/admin/v1/diagnostics", "/api/admin/v1/capabilities"} {
		response := authorizedRequest(t, handler, cookie, endpoint)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"api_version":"1"`) {
			t.Fatalf("endpoint %s status=%d body=%s", endpoint, response.Code, response.Body.String())
		}
	}

	unauthorized := request(t, handler, http.MethodGet, "/api/admin/v1/snapshot", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized || !strings.Contains(unauthorized.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
}

func TestObservationSnapshotGolden(t *testing.T) {
	fixed := time.Date(2026, 8, 25, 10, 30, 0, 0, time.UTC)
	snapshot := FinalizeSnapshot(ObservationSnapshot{
		Service: ServiceObservation{Version: "v2.0.0", Protocol: "admin.v1", Running: true, AdminSecure: true},
		Caches:  []CacheObservation{{CacheRef: "cache-abc123", PathFingerprint: "sha256:abc123", Readiness: "ready", SchemaVersion: 17, WALCapable: true, JournalMode: "wal", RepositoryCount: 1, Repositories: []RepositoryObservation{{RepoID: "example/repo", BindingState: "bound", Coverage: CoverageObservation{Head: CoverageLane{State: "current", Status: "complete"}, Tail: CoverageLane{State: "partial", Status: "bounded"}, Secondary: CoverageLane{State: "current", Status: "complete"}, Projection: CoverageLane{State: "current", Status: "current", CurrentGeneration: 4, CoveredGeneration: 4}, RAG: CoverageLane{State: "current", Status: "ready", CurrentGeneration: 4, CoveredGeneration: 4}}}}}},
	}, fixed)
	actual, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/snapshot.golden.json")
	if err != nil {
		t.Fatalf("%v\nactual:\n%s", err, actual)
	}
	if !bytes.Equal(append(actual, '\n'), expected) {
		t.Fatalf("snapshot contract changed\nactual:\n%s\nexpected:\n%s", actual, expected)
	}
}

func TestEventReplayAndExpiredCursor(t *testing.T) {
	c := New(Config{Assets: fstest.MapFS{"index.html": {Data: []byte("index")}}, EventCapacity: 2})
	cookie := authorizeObservationController(c, time.Now().Add(time.Hour))
	first := c.events.append(Event{Kind: "job_changed", EntityType: "job", EntityID: "job-1"})
	c.events.append(Event{Kind: "job_changed", EntityType: "job", EntityID: "job-2"})
	c.events.append(Event{Kind: "snapshot_changed", EntityType: "snapshot", EntityID: "overview"})
	c.events.append(Event{Kind: "job_changed", EntityType: "job", EntityID: "job-3"})

	expired := authorizedRequest(t, c.handler(), cookie, "/api/admin/v1/events?after="+first.Cursor)
	if expired.Code != http.StatusOK || !strings.Contains(expired.Body.String(), "event: snapshot_required") {
		t.Fatalf("expired stream status=%d body=%s", expired.Code, expired.Body.String())
	}
	future := authorizedRequest(t, c.handler(), cookie, "/api/admin/v1/events?after="+encodeEventCursor(999))
	if future.Code != http.StatusOK || !strings.Contains(future.Body.String(), "event: snapshot_required") {
		t.Fatalf("future stream status=%d body=%s", future.Code, future.Body.String())
	}

	retained := c.events.events[len(c.events.events)-2]
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/admin/v1/events?after="+retained.Cursor, nil).WithContext(ctx)
	req.AddCookie(cookie)
	recorder := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		c.handler().ServeHTTP(recorder, req)
		close(done)
	}()
	select {
	case <-recorder.flushed:
	case <-time.After(time.Second):
		t.Fatal("event replay did not flush")
	}
	cancel()
	<-done
	body := recorder.String()
	if !strings.Contains(body, `"entity_id":"job-3"`) || strings.Contains(body, `"entity_id":"job-2"`) {
		t.Fatalf("retained replay body=%s", body)
	}
}

func TestObservationPollPublishesCompactInvalidation(t *testing.T) {
	var mu sync.Mutex
	revision := "snapshot-one"
	provider := func(context.Context) (ObservationSnapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		return ObservationSnapshot{APIVersion: "1", Revision: revision}, nil
	}
	c := New(Config{Snapshot: provider, EventPollInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.pollObservation(ctx)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	var baseline uint64
	for time.Now().Before(deadline) {
		_, _, latest, _ := c.events.replay(0)
		if latest > 0 {
			baseline = latest
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if baseline == 0 {
		cancel()
		<-done
		t.Fatal("initial snapshot invalidation was not published")
	}
	mu.Lock()
	revision = "snapshot-two"
	mu.Unlock()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events, _, _, _ := c.events.replay(baseline)
		if len(events) > 0 {
			if events[0].Kind != "snapshot_changed" || events[0].Revision != "snapshot-two" {
				t.Fatalf("event=%+v", events[0])
			}
			cancel()
			<-done
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("snapshot invalidation was not published")
}

func TestEventStreamEndsAtSessionExpiry(t *testing.T) {
	c := New(Config{Assets: fstest.MapFS{"index.html": {Data: []byte("index")}}})
	cookie := authorizeObservationController(c, time.Now().Add(25*time.Millisecond))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/admin/v1/events", nil)
	req.AddCookie(cookie)
	recorder := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		c.handler().ServeHTTP(recorder, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event stream outlived its bounded session")
	}
}

func authorizeObservationController(c *Controller, expires time.Time) *http.Cookie {
	value := "observation-session"
	c.sessions[sha256.Sum256([]byte(value))] = session{Expires: expires, CSRF: "csrf"}
	return &http.Cookie{Name: sessionCookie, Value: value}
}

func authorizedRequest(t *testing.T, handler http.Handler, cookie *http.Cookie, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+target, nil)
	req.AddCookie(cookie)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	return result
}

type flushRecorder struct {
	header  http.Header
	mu      sync.Mutex
	body    bytes.Buffer
	status  int
	flushed chan struct{}
	once    sync.Once
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: make(http.Header), flushed: make(chan struct{})}
}

func (r *flushRecorder) Header() http.Header { return r.header }

func (r *flushRecorder) WriteHeader(status int) { r.status = status }

func (r *flushRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(data)
}

func (r *flushRecorder) Flush() { r.once.Do(func() { close(r.flushed) }) }

func (r *flushRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}
