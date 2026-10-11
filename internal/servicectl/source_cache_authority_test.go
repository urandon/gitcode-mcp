package servicectl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
)

func TestAdminSourceAuthorityDoesNotCrossActualCacheReplacement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	path := filepath.Join(root, "cache.db")
	binding := cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	store, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal("source cache fixture unavailable")
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.AddRepository(ctx, binding) != nil {
		t.Fatal("source binding fixture unavailable")
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal("source cache identity unavailable")
	}
	cfg := adminFakeRAGConfig()
	cfg.CachePath, cfg.LockPath = path, path+".lock"
	manager := Manager{Source: navigationSource{root}, RuntimeDir: filepath.Join(root, "runtime"), AdminCachePath: path, EffectiveConfig: &cfg}
	jobs := NewJobManager(filepath.Join(root, "jobs.json"))
	m := NewMaintenanceManager(manager, jobs, filepath.Join(root, "registry.json"))
	enroll := testMaintenanceEnrollRequest(path, "source-cache-control", MaintenancePolicy{SyncMode: "off"})
	enroll.ConfigSnapshot, enroll.ConfigHash = cfg, maintenanceHash(cfg)
	entry, err := m.Enroll(ctx, enroll)
	if err != nil {
		t.Fatal("source cache enrollment unavailable")
	}
	repo := createRepositoryDocsRegistrationRepo(t, filepath.Join(root, "source"), "synthetic-private-parent-control")
	registered, ok, err := m.RegisterRepositoryDocsSource(ctx, identity.UUID, entry.RepoID, repo, "")
	if err != nil || !ok || registered.RepositoryDocs == nil {
		t.Fatal("source cache registration unavailable")
	}
	read := func() adminhttp.ObservationSnapshot {
		t.Helper()
		s, err := manager.adminObservation(ctx, jobs, m, time.Now().UTC())
		if err != nil || len(s.Caches) != 1 || len(s.Caches[0].Repositories) != 1 {
			t.Fatal("source cache snapshot unavailable")
		}
		data, err := json.Marshal(s)
		if err != nil || strings.Contains(string(data), root) || strings.Contains(string(data), identity.UUID) || strings.Contains(string(data), "synthetic-private") {
			t.Fatal("source cache snapshot leaked private authority")
		}
		return s
	}
	assertCurrentSource := func(s adminhttp.ObservationSnapshot) {
		t.Helper()
		docs := s.Caches[0].Repositories[0].Documentation
		if docs == nil || len(docs.Sources) != 1 || docs.Sources[0].SourceID != registered.RepositoryDocs.SourceRegistrationID || docs.Sources[0].ObservationRefs == nil || docs.Sources[0].ObservationRefs.Cache != s.Caches[0].ObservationRefs.Cache {
			t.Fatal("current physical source parent control failed")
		}
	}
	before := read()
	assertCurrentSource(before)
	if store.Close() != nil || os.Rename(path, path+".retained") != nil {
		t.Fatal("actual cache replacement boundary unavailable")
	}
	replacement, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal("replacement cache unavailable")
	}
	t.Cleanup(func() { _ = replacement.Close() })
	if replacement.AddRepository(ctx, binding) != nil {
		t.Fatal("replacement binding unavailable")
	}
	newIdentity, err := replacement.CacheIdentity(ctx)
	if err != nil || newIdentity.UUID == identity.UUID {
		t.Fatal("distinct physical replacement precondition failed")
	}
	after := read()
	docs := after.Caches[0].Repositories[0].Documentation
	if after.Caches[0].ObservationRefs.Cache == before.Caches[0].ObservationRefs.Cache || docs == nil || len(docs.Sources) != 0 || docs.Registered || docs.ObservationRefs != nil || docs.SearchAvailable || docs.IndexHandoff != "" {
		t.Fatal("replacement cache inherited historical source authority")
	}
	if len(after.Maintenance) != 1 || len(after.Jobs) != 0 || len(m.sources[entry.RegistrationID]) != 1 || m.entries[entry.RegistrationID].CacheUUID != identity.UUID {
		t.Fatal("observation changed retained maintenance or job authority")
	}
	// Restoring the original physical authority makes its exact source visible
	// again without enrollment, provider traffic or observation-triggered repair.
	if replacement.Close() != nil || os.Rename(path, path+".replacement") != nil || os.Rename(path+".retained", path) != nil {
		t.Fatal("original cache restore boundary unavailable")
	}
	assertCurrentSource(read())
}

func TestAdminEntryRequiresKnownCurrentPhysicalCache(t *testing.T) {
	entries := []adminMaintenanceEntry{{path: "cache", entry: MaintenanceEntry{CacheUUID: "old", RepoID: "owner/repo"}}, {path: "cache", entry: MaintenanceEntry{CacheUUID: "current", RepoID: "owner/repo"}}}
	if findAdminEntry(entries, "cache", "owner/repo", "") != nil || findAdminEntry(entries, "cache", "owner/repo", "absent") != nil {
		t.Fatal("unknown physical cache acquired maintenance authority")
	}
	current := findAdminEntry(entries, "cache", "owner/repo", "current")
	if current == nil || current.CacheUUID != "current" {
		t.Fatal("historical same-path entry shadowed current cache")
	}
}
