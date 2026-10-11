package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/observability"
)

func observationResolutionRequest(t *testing.T, m *MaintenanceManager, conflict MaintenanceEntry, selected MaintenanceIdentityCandidate, key string) MaintenanceConflictResolutionRequest {
	t.Helper()
	req := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: selected.CandidateRef, ExpectedGeneration: conflict.Generation}
	plan, err := m.PlanConflictResolution(context.Background(), req)
	if err != nil || plan.Status != "ready" || plan.PlanID == "" {
		t.Fatal("actual resolution plan unavailable")
	}
	req.PlanID, req.IdempotencyKey = plan.PlanID, key
	return req
}

func observationIdentityRequest(t *testing.T, m *MaintenanceManager, enabled bool) (MaintenanceEntry, MaintenanceConflictResolutionRequest) {
	t.Helper()
	listed, err := m.List(context.Background())
	if err != nil || len(listed.Entries) != 1 || listed.Entries[0].IdentityConflict == nil {
		t.Fatal("actual identity conflict fixture unavailable")
	}
	conflict := listed.Entries[0]
	for _, candidate := range conflict.IdentityConflict.Candidates {
		if candidate.WasEnabled == enabled {
			return conflict, observationResolutionRequest(t, m, conflict, candidate, "synthetic-private-resolution-key")
		}
	}
	t.Fatal("actual identity policy selection unavailable")
	return MaintenanceEntry{}, MaintenanceConflictResolutionRequest{}
}

func requireResolutionEvent(t *testing.T, event observability.Event, predecessor, result MaintenanceEntry) {
	t.Helper()
	after := maintenanceObservedIdentityState(&result)
	before := maintenanceObservedIdentityState(&predecessor)
	if event.MaintenanceScope != "identity_resolution" || event.PreviousRegistrationRef != expectedObservationRef("registration", predecessor.RegistrationID) || event.RegistrationRef != expectedObservationRef("registration", result.RegistrationID) || event.CacheRef != expectedObservationRef("cache", result.CacheUUID) || event.RepoRef != expectedObservationRef("repo", result.RepoID) || event.PreviousState != before.state || event.PreviousFailureClass != before.failure || event.Outcome != after.state || event.FailureClass != after.failure || event.SourceRef != "" || event.JobRef != "" || event.StageRef != "" || event.CorrelationRef == "" {
		t.Fatal("resolution event did not describe confirmed predecessor and actual result")
	}
}

func TestMaintenanceResolutionObservationActualIdentityAdminReplayAndRestart(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			m, registry, _ := newMaintenanceIdentityConflictFixture(t)
			c := maintenanceEventCollector(t)
			m.jobs.SetObservation(c)
			conflict, req := observationIdentityRequest(t, m, enabled)
			stale := req
			stale.PlanID, stale.IdempotencyKey = "wrong-plan", "synthetic-stale-key"
			if _, err := m.ApplyConflictResolution(context.Background(), stale); err == nil {
				t.Fatal("stale resolution plan unexpectedly applied")
			}
			// This Admin owner deliberately calls Apply directly; there is no
			// outer Admin-control receipt manager on the resolution path.
			controls := &AdminControlManager{maintenance: m}
			mapped := adminhttp.MaintenanceConflictResolutionRequest{RegistrationID: req.RegistrationID, CandidateRef: req.CandidateRef, ExpectedGeneration: req.ExpectedGeneration, PlanID: req.PlanID, IdempotencyKey: req.IdempotencyKey}
			value, err := controls.ApplyMaintenanceConflictResolution(context.Background(), mapped)
			if err != nil {
				t.Fatal("actual Admin resolution unavailable")
			}
			result := value.(MaintenanceConflictResolutionResult)
			if result.Replayed || result.Outcome != "resolved" {
				t.Fatal("actual resolution result changed")
			}
			after, err := m.List(context.Background())
			if err != nil || len(after.Entries) != 1 || after.Entries[0].Enabled != enabled || after.Entries[0].IdentityConflict != nil || after.Entries[0].Policy.SyncEnabled != enabled {
				t.Fatal("selected identity policy changed")
			}
			if replay, err := m.ApplyConflictResolution(context.Background(), req); err != nil || !replay.Replayed || replay.ReceiptID != result.ReceiptID {
				t.Fatal("actual resolution replay changed")
			}
			restarted := NewMaintenanceManager(m.manager, m.jobs, registry)
			if restarted.Load() != nil {
				t.Fatal("resolved registry restart unavailable")
			}
			if replay, err := restarted.ApplyConflictResolution(context.Background(), req); err != nil || !replay.Replayed {
				t.Fatal("durable resolution replay changed")
			}
			events := maintenanceObservationPage(t, c, 1)
			for _, event := range events {
				requireResolutionEvent(t, event, conflict, after.Entries[0])
			}
			data, _ := json.Marshal(events)
			for _, private := range []string{registry, req.IdempotencyKey, req.CandidateRef, req.PlanID, req.RegistrationID, after.Entries[0].RegistrationID, after.Entries[0].RepoID, after.Entries[0].CacheUUID, "selected-config", "other-config"} {
				if strings.Contains(string(data), private) {
					t.Fatal("private resolution coordinates entered diagnostics")
				}
			}
		})
	}
}

func TestMaintenanceResolutionObservationActualSaveRollbackAndPanic(t *testing.T) {
	for _, mode := range []string{"rollback", "panic", "rename_readback"} {
		t.Run(mode, func(t *testing.T) {
			m, _, _ := newMaintenanceIdentityConflictFixture(t)
			conflict, req := observationIdentityRequest(t, m, true)
			c := maintenanceEventCollector(t)
			m.jobs.SetObservation(c)
			ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
			// Real earlier owners share this outer diagnostic carrier. A later
			// resolution rollback must not erase their confirmed work or loss.
			path := m.entries[conflict.RegistrationID].cachePath
			earlierReq := testMaintenanceEnrollRequest(path, "earlier-owner-key", MaintenancePolicy{SyncMode: "off"})
			earlierReq.RepoID = "owner/earlier"
			store, err := cache.NewSQLiteStore(ctx, path)
			if err != nil {
				t.Fatal("earlier owner cache fixture unavailable")
			}
			if store.AddRepository(ctx, cache.RepositoryBinding{RepoID: earlierReq.RepoID, Owner: "owner", Name: "earlier"}) != nil || store.Close() != nil {
				t.Fatal("earlier owner binding unavailable")
			}
			earlier, err := m.Enroll(ctx, earlierReq)
			if err != nil {
				t.Fatal("earlier actual enrollment unavailable")
			}
			repo := createRepositoryDocsRegistrationRepo(t, filepath.Join(filepath.Dir(path), "earlier-repo"), "synthetic-private-source")
			if _, ok, err := m.RegisterRepositoryDocsSource(ctx, earlier.CacheUUID, earlier.RepoID, repo, ""); err != nil || !ok {
				t.Fatal("earlier actual source registration unavailable")
			}
			buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
			buffer.lost = 2 // Earlier known omissions, not resolution attempts.
			baseline := *buffer
			writes := 0
			m.writeFile = func(name string, data []byte, permissions os.FileMode) error {
				writes++
				if mode == "panic" {
					panic("synthetic-private-storage-panic")
				}
				if mode == "rename_readback" && durableAtomicWriteFile(name, data, permissions) != nil {
					return errors.New("fixture durable write unavailable")
				}
				return errors.New("synthetic-private-storage-failure")
			}
			panicked := false
			var applyErr error
			func() {
				defer func() { panicked = recover() != nil }()
				_, applyErr = m.ApplyConflictResolution(ctx, req)
			}()
			if writes != 1 || panicked != (mode == "panic") || (mode == "rollback" && applyErr == nil) || (mode == "rename_readback" && applyErr != nil) {
				t.Fatal("actual save, panic, or ambiguous readback behavior changed")
			}
			if mode != "rename_readback" {
				if *buffer != baseline {
					t.Fatal("unconfirmed attempt modified earlier captures or loss")
				}
				if mode == "rollback" {
					if m.entries[conflict.RegistrationID].IdentityConflict == nil || len(m.resolutionReceipts) != 0 {
						t.Fatal("actual registry rollback changed")
					}
					m.writeFile = durableAtomicWriteFile
					if _, err := m.ApplyConflictResolution(ctx, req); err != nil {
						t.Fatal("actual retry after rollback unavailable")
					}
				}
			}
			want := baseline.count + int(baseline.lost)
			if mode != "panic" {
				want++
			}
			flush()
			events := maintenanceObservationPage(t, c, want)
			for _, event := range events {
				if event.MaintenanceScope == "identity_resolution" && mode == "panic" {
					t.Fatal("panic certified unconfirmed resolution")
				}
			}
		})
	}
}

// Real SQLite clones and legacy disk rows exercise the existing cohort owner,
// including final per-repository canonicalization. No mock result enumeration.
func observationCloneFixture(t *testing.T, mode string) (*MaintenanceManager, MaintenanceEntry, MaintenanceConflictResolutionRequest) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.db"), filepath.Join(dir, "second.db")
	store, err := cache.NewSQLiteStore(ctx, first)
	if err != nil {
		t.Fatal("actual clone cache unavailable")
	}
	for _, binding := range []cache.RepositoryBinding{{RepoID: "owner/first", Owner: "owner", Name: "first", Aliases: []string{"legacy/first"}}, {RepoID: "owner/second", Owner: "owner", Name: "second"}} {
		if store.AddRepository(ctx, binding) != nil {
			t.Fatal("actual clone binding unavailable")
		}
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil || store.Close() != nil {
		t.Fatal("actual clone identity unavailable")
	}
	data, err := os.ReadFile(first)
	if err != nil || os.WriteFile(second, data, 0o600) != nil {
		t.Fatal("actual physical clone unavailable")
	}
	var entries []maintenanceDiskEntry
	for _, path := range []string{first, second} {
		cfg := config.Default()
		cfg.CachePath = path
		if mode == "config_invalid" {
			cfg.CachePath = filepath.Dir(path) + "/./" + filepath.Base(path)
		}
		for _, repoID := range []string{"owner/first", "owner/second"} {
			enabled := repoID == "owner/first"
			policy := MaintenancePolicy{SyncMode: "off"}
			if enabled {
				policy = MaintenancePolicy{SyncEnabled: true, SyncMode: "head", Issues: true}
			}
			entries = append(entries, maintenanceDiskEntry{MaintenanceEntry: MaintenanceEntry{RegistrationID: maintenanceRegistrationID(identity.UUID, repoID), CacheUUID: identity.UUID, RepoID: repoID, Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: enabled, Generation: 2}, CachePath: path, ConfigSnapshot: cfg})
		}
		if mode == "residual" {
			entries = append(entries, maintenanceDiskEntry{MaintenanceEntry: MaintenanceEntry{RegistrationID: maintenanceRegistrationID(identity.UUID, "legacy/first"), CacheUUID: identity.UUID, RepoID: "legacy/first", Policy: MaintenancePolicy{SyncMode: "off"}, ConfigHash: maintenanceHash(cfg), Enabled: false}, CachePath: path, ConfigSnapshot: cfg})
		}
	}
	data, err = json.Marshal(maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: entries})
	registry := filepath.Join(dir, "registry.json")
	if err != nil || os.WriteFile(registry, data, 0o600) != nil {
		t.Fatal("actual clone registry unavailable")
	}
	m := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registry)
	if m.Load() != nil {
		t.Fatal("actual clone recovery unavailable")
	}
	listed, err := m.List(ctx)
	if err != nil || len(listed.Entries) != 1 || listed.Entries[0].IdentityConflict == nil || listed.Entries[0].State != "cache_clone_conflict" {
		t.Fatal("actual clone cohort unavailable")
	}
	conflict := listed.Entries[0]
	for _, candidate := range conflict.IdentityConflict.Candidates {
		if candidate.PathFingerprint == pathFingerprint(maintenanceCanonicalPathKey(first)) {
			return m, conflict, observationResolutionRequest(t, m, conflict, candidate, "synthetic-clone-resolution-key")
		}
	}
	t.Fatal("actual physical clone selection unavailable")
	return nil, MaintenanceEntry{}, MaintenanceConflictResolutionRequest{}
}

func TestMaintenanceResolutionObservationActualCloneFinalResults(t *testing.T) {
	for _, mode := range []string{"distinct", "residual", "unresolved", "timeout", "config_invalid"} {
		t.Run(mode, func(t *testing.T) {
			m, conflict, req := observationCloneFixture(t, mode)
			if mode == "unresolved" || mode == "timeout" {
				inspect := m.inspectCache
				m.inspectCache = func(ctx context.Context, path, repoID string) (cache.CacheIdentity, cache.RepositoryBinding, error) {
					if repoID == "owner/first" {
						if mode == "timeout" {
							return cache.CacheIdentity{}, cache.RepositoryBinding{}, errMaintenanceCacheInspectionTimeout
						}
						return cache.CacheIdentity{}, cache.RepositoryBinding{}, errors.New("synthetic-private-inspection-failure")
					}
					return inspect(ctx, path, repoID)
				}
			}
			if mode == "config_invalid" {
				// Plan candidate inspection uses the real cache; this existing
				// canonicalization hook fails snapshot validation only afterwards.
				canonical := m.canonicalizePath
				m.canonicalizePath = func(path string) (string, error) {
					if strings.Contains(path, "/./") {
						return "", errors.New("synthetic-private-snapshot-failure")
					}
					return canonical(path)
				}
			}
			c := maintenanceEventCollector(t)
			m.jobs.SetObservation(c)
			ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
			result, err := m.ApplyConflictResolution(ctx, req)
			if err != nil || len(result.RegistrationIDs) != 2 || result.RetiredClonePaths != 1 {
				t.Fatal("actual clone resolution result changed")
			}
			buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
			if buffer.count != 2 || buffer.lost != 0 {
				t.Fatal("final distinct clone results were not captured once each")
			}
			wantFirst := map[string]string{"distinct": "enrolled", "residual": "identity_conflict", "unresolved": "identity_unresolved", "timeout": "cache_inspection_timeout", "config_invalid": "config_snapshot_invalid"}[mode]
			seenFirst := false
			byRef := map[string]MaintenanceEntry{}
			for _, id := range result.RegistrationIDs {
				entry := *m.entries[id]
				byRef[expectedObservationRef("registration", id)] = entry
				if entry.RepoID == "owner/first" {
					seenFirst = true
					if entry.State != wantFirst || (mode != "distinct" && entry.Enabled) {
						t.Fatal("actual residual clone state was replaced by provisional success")
					}
				}
			}
			if !seenFirst {
				t.Fatal("actual first repository result missing")
			}
			if replay, err := m.ApplyConflictResolution(ctx, req); err != nil || !replay.Replayed || buffer.count != 2 {
				t.Fatal("clone replay produced fresh observation")
			}
			flush()
			events := maintenanceObservationPage(t, c, 2)
			correlation := ""
			for _, event := range events {
				requireResolutionEvent(t, event, conflict, byRef[event.RegistrationRef])
				if correlation != "" && correlation != event.CorrelationRef {
					t.Fatal("one clone operation lost shared correlation")
				}
				correlation = event.CorrelationRef
			}
		})
	}
}

func TestMaintenanceResolutionObservationActualFlushAfterRedirectFence(t *testing.T) {
	m, _, _ := newMaintenanceIdentityConflictFixture(t)
	conflict, req := observationIdentityRequest(t, m, false)
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	written, continueSave, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	m.writeFile = func(name string, payload []byte, permissions os.FileMode) error {
		err := durableAtomicWriteFile(name, payload, permissions)
		close(written)
		<-continueSave
		return err
	}
	go func() { _, err := m.ApplyConflictResolution(context.Background(), req); done <- err }()
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		close(continueSave)
		t.Fatal("actual resolution did not reach durable save")
	}
	// The existing cache mutation fence is already acquired. Hold its job
	// authority mutex after save to block the real redirect-publication owner.
	m.jobs.mu.Lock()
	var once sync.Once
	release := func() { once.Do(m.jobs.mu.Unlock) }
	defer release()
	close(continueSave)
	finish := func() {
		t.Helper()
		release()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("actual resolution failed after fence release")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("actual resolution fence did not unwind")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for !m.mu.TryLock() {
		if time.Now().After(deadline) {
			finish()
			t.Fatal("actual resolution registry lock did not unwind")
		}
		time.Sleep(time.Millisecond)
	}
	resolved := m.entries[m.resolveRegistrationIDLocked(conflict.RegistrationID)]
	disabled := resolved != nil && resolved.State == "disabled"
	m.mu.Unlock()
	if !m.jobs.cacheMutationFences[conflict.CacheUUID] {
		finish()
		t.Fatal("actual cache mutation fence disappeared before redirect publication")
	}
	// A durable FIFO marker rules out an empty query racing the async writer.
	marker := observability.KnownRef("correlation", "fixture-held-resolution-fence")
	if !c.Emit(observability.RecoveryFinished, observability.Context{Correlation: marker}) {
		finish()
		t.Fatal("resolution fence marker not admitted")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		page, err := c.Query(observability.Filter{Code: observability.RecoveryFinished}, "", 10)
		if err != nil {
			finish()
			t.Fatal("resolution fence marker query unavailable")
		}
		if len(page.Events) == 1 {
			break
		}
		if time.Now().After(deadline) {
			finish()
			t.Fatal("resolution fence marker was not persisted")
		}
		time.Sleep(time.Millisecond)
	}
	page, err := c.Query(observability.Filter{Code: observability.MaintenanceStateChanged}, "", 10)
	finish()
	if !disabled || err != nil || len(page.Events) != 0 || page.Coverage.Dropped != 0 {
		t.Fatal("resolution observation crossed held redirect/cache fence")
	}
	m.jobs.mu.Lock()
	fenced := m.jobs.cacheMutationFences[conflict.CacheUUID]
	m.jobs.mu.Unlock()
	if fenced {
		t.Fatal("actual cache mutation fence was not released")
	}
	maintenanceObservationPage(t, c, 1)
}

func TestMaintenanceResolutionObservationActualSharedBudgetAndOperationGroups(t *testing.T) {
	for _, mode := range []string{"mixed_overflow", "repeated_predecessor"} {
		t.Run(mode, func(t *testing.T) {
			m, conflict, req := observationCloneFixture(t, "distinct")
			original, err := os.ReadFile(m.path)
			if err != nil {
				t.Fatal("initial clone registry fixture unavailable")
			}
			c := maintenanceEventCollector(t)
			m.jobs.SetObservation(c)
			ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
			buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
			if mode == "mixed_overflow" {
				// Exercise all three scopes against the shared primitive limit;
				// actual clone Apply qualifies its two final results afterwards.
				entry := &MaintenanceEntry{RegistrationID: "synthetic-earlier", CacheUUID: conflict.CacheUUID, RepoID: "owner/earlier", State: "enrolled"}
				captureMaintenanceObservation(ctx, maintenanceObservationState{state: "absent"}, entry, true)
				source := &repositoryDocsRegisteredSource{State: RepositoryDocsMaintenanceState{SourceRegistrationID: "synthetic-earlier-source", State: "registered"}}
				for i := 1; i < maxMaintenanceObservations-1; i++ {
					captureMaintenanceSourceObservation(ctx, maintenanceObservationState{state: "absent"}, entry, source, true)
				}
			}
			first, err := m.ApplyConflictResolution(ctx, req)
			if err != nil || len(first.RegistrationIDs) != 2 {
				t.Fatal("actual first clone resolution unavailable")
			}
			if mode == "mixed_overflow" {
				if buffer.count != maxMaintenanceObservations || buffer.lost != 1 {
					t.Fatal("qualified clone overflow did not share bounded capacity")
				}
				// An unchanged finite pair is not a qualified omission, even
				// at full capacity; an unsupported changed result is honest loss.
				pending := newMaintenanceResolutionObservation(ctx, &conflict)
				pending.record(&conflict)
				unknown := conflict
				unknown.State = "synthetic-private-state"
				pending.record(&unknown)
				pending.confirm()
				if buffer.count != maxMaintenanceObservations || buffer.lost != 2 {
					t.Fatal("unchanged or unsupported pair fabricated retained evidence")
				}
				flush()
				maintenanceObservationPage(t, c, maxMaintenanceObservations+2)
				return
			}
			// Restore the real persisted conflict fixture as a later episode.
			// Load is a projection, not a new event. The same deterministic
			// predecessor ID must not collapse distinct confirmed operations.
			if os.WriteFile(m.path, original, 0o600) != nil {
				t.Fatal("later conflict episode fixture unavailable")
			}
			restarted := NewMaintenanceManager(m.manager, m.jobs, m.path)
			if restarted.Load() != nil {
				t.Fatal("later actual conflict episode load unavailable")
			}
			req.IdempotencyKey = "synthetic-later-resolution-key"
			if _, err := restarted.ApplyConflictResolution(ctx, req); err != nil || buffer.count != 4 || buffer.resolutions != 2 {
				t.Fatal("later confirmed operation group unavailable")
			}
			flush()
			events := maintenanceObservationPage(t, c, 4)
			groups := map[string]int{}
			for _, event := range events {
				if event.PreviousRegistrationRef != expectedObservationRef("registration", conflict.RegistrationID) {
					t.Fatal("repeated episode redirected historical predecessor")
				}
				groups[event.CorrelationRef]++
			}
			if len(events) == 4 && (len(groups) != 2) {
				t.Fatal("distinct confirmed operations shared correlation")
			}
			for _, count := range groups {
				if count > 2 {
					t.Fatal("operation correlation included another episode")
				}
			}
		})
	}
}

func TestMaintenanceResolutionObservationMetricsOnlyHasNoEventLoss(t *testing.T) {
	m, _, req := observationCloneFixture(t, "distinct")
	_, metrics := testJobMetrics(t, "")
	m.jobs.SetMetrics(metrics)
	ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
	buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
	if _, err := m.ApplyConflictResolution(ctx, req); err != nil || buffer.count != 0 || buffer.lost != 0 {
		t.Fatal("metrics-only resolution fabricated missing event telemetry")
	}
	flush()
	if metrics.Snapshot().Dropped != 0 {
		t.Fatal("disabled event stream counted false resolution loss")
	}
}
