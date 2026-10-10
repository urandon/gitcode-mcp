package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

type bootstrapFixtureClient struct {
	*fakeGitCodeClient
	mu       sync.Mutex
	labels   []gitcode.RepositoryLabel
	writes   atomic.Int32
	writeErr error
	onWrite  func()
}

func (c *bootstrapFixtureClient) GetRepositoryMetadata(context.Context, gitcode.RepoRequest) (gitcode.RepositoryMetadata, error) {
	return gitcode.RepositoryMetadata{ProviderID: "17", Private: true, Owner: "example-owner", Name: "example-repo", DefaultBranch: "main"}, nil
}
func (c *bootstrapFixtureClient) ListRepositoryLabels(context.Context, gitcode.RepoRequest) (gitcode.RepositoryLabels, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return gitcode.RepositoryLabels{RepositoryID: "17", Labels: append([]gitcode.RepositoryLabel(nil), c.labels...)}, nil
}
func (c *bootstrapFixtureClient) CreateRepositoryLabel(_ context.Context, r gitcode.RepositoryLabelRequest, _ gitcode.WriteOptions) (gitcode.WriteResult[gitcode.RepositoryLabel], error) {
	c.writes.Add(1)
	if c.onWrite != nil {
		c.onWrite()
	}
	label := gitcode.RepositoryLabel{ID: json.Number("7"), Name: r.Name, Color: r.Color, RepositoryID: "17"}
	c.mu.Lock()
	c.labels = append(c.labels, label)
	c.mu.Unlock()
	if c.writeErr != nil {
		return gitcode.WriteResult[gitcode.RepositoryLabel]{}, c.writeErr
	}
	return gitcode.WriteResult[gitcode.RepositoryLabel]{Record: label, Confirmed: true, RemoteID: "7"}, nil
}
func bootstrapRequest() WriteCommandRequest {
	return WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Label: "state:ready", Color: "#AABBCC", IdempotencyKey: "bootstrap-label"}
}
func bootstrapFixture(t *testing.T) (*cache.SQLiteStore, *bootstrapFixtureClient) {
	t.Helper()
	t.Setenv("GITCODE_TOKEN", "fixture-token")
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedStore(t, context.Background(), store)
	return store, &bootstrapFixtureClient{fakeGitCodeClient: &fakeGitCodeClient{}, labels: []gitcode.RepositoryLabel{{ID: "6", Name: "unrelated", Color: "#000000", RepositoryID: "17"}}}
}
func TestIssue157DurableLabelClaimAndPublicationRecovery(t *testing.T) {
	for _, scenario := range []string{"confirmed", "ambiguous", "cache_failed", "claim_failed"} {
		t.Run(scenario, func(t *testing.T) {
			store, client := bootstrapFixture(t)
			fault := &wikiFaultStore{SQLiteStore: store}
			ctx := context.Background()
			if scenario == "ambiguous" {
				client.writeErr = errors.New("opaque-provider-body")
			}
			if scenario == "cache_failed" {
				fault.graphErr = errors.New("fixture-publication-fault")
			}
			if scenario == "claim_failed" {
				fault.claimErr = errors.New("fixture-claim-fault")
			}
			client.onWrite = func() {
				e, err := store.GetAuditEventByKey(ctx, "fixture-a", "bootstrap-label")
				if err != nil || e == nil || e.Status != audit.StatusInProgress {
					t.Error("POST before durable claim")
				}
			}
			result, err := NewWithClient(fault, client).CreateRepositoryLabel(ctx, bootstrapRequest())
			if scenario == "claim_failed" {
				if err == nil || client.writes.Load() != 0 {
					t.Fatal("failed audit allowed write")
				}
				return
			}
			if scenario != "confirmed" && err == nil {
				t.Fatal("failure was reported as success")
			}
			if scenario == "confirmed" && (err != nil || result.ID != "LABEL-7") {
				t.Fatalf("label publication failed: status=%s error=%v", result.Status, err)
			}
			fault.graphErr = nil
			client.writeErr = nil
			recovered, err := NewWithClient(fault, client).CreateRepositoryLabel(ctx, bootstrapRequest())
			if err != nil || client.writes.Load() != 1 {
				t.Fatalf("recovery failed: writes=%d error=%v", client.writes.Load(), err)
			}
			if scenario != "confirmed" && recovered.Status != "recovered_after_ambiguous_write" {
				t.Fatal("missing recovery evidence")
			}
			settled, err := NewWithClient(fault, client).CreateRepositoryLabel(ctx, bootstrapRequest())
			if err != nil || settled.Status != "already_applied" || !settled.Replayed || client.writes.Load() != 1 {
				t.Fatal("settled replay unsafe")
			}
			if settled.RemoteRevision == "" || settled.RemoteRevision != recovered.RemoteRevision {
				t.Fatal("settled replay lost canonical revision")
			}
			source, err := store.GetSourceScoped(ctx, "fixture-a", "LABEL-7")
			if err != nil || source.Provenance != cache.ProvenanceLive || source.Kind != "label" {
				t.Fatal("canonical cache readback missing")
			}
			entry, _ := store.GetAuditEventByKey(ctx, "fixture-a", "bootstrap-label")
			if entry == nil || entry.Status != audit.StatusSucceeded {
				t.Fatal("audit not settled")
			}
			changed := bootstrapRequest()
			changed.Color = "#000000"
			_, err = NewWithClient(fault, client).CreateRepositoryLabel(ctx, changed)
			if err == nil || client.writes.Load() != 1 {
				t.Fatal("same-key conflicting color was accepted")
			}
			if len(client.labels) != 2 || client.labels[0].Name != "unrelated" {
				t.Fatal("unrelated labels changed")
			}
		})
	}
}
func TestIssue157ConcurrentLabelClaim(t *testing.T) {
	store, client := bootstrapFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	client.onWrite = func() { close(entered); <-release }
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = NewWithClient(store, client).CreateRepositoryLabel(context.Background(), bootstrapRequest())
	}()
	<-entered
	_, err := NewWithClient(store, client).CreateRepositoryLabel(context.Background(), bootstrapRequest())
	if err == nil {
		t.Error("concurrent uncertain write became success")
	}
	close(release)
	wg.Wait()
	if client.writes.Load() != 1 {
		t.Fatal("concurrent caller duplicated POST")
	}
}
func TestIssue157ExistingLabelAndValidationAreNonMutating(t *testing.T) {
	store, client := bootstrapFixture(t)
	client.labels = append(client.labels, gitcode.RepositoryLabel{ID: "7", Name: "state:ready", Color: "#aabbcc", RepositoryID: "17"})
	requests := []WriteCommandRequest{bootstrapRequest()}
	missing := bootstrapRequest()
	missing.IdempotencyKey = ""
	requests = append(requests, missing)
	description := bootstrapRequest()
	description.Description = "unsupported"
	requests = append(requests, description)
	invalid := bootstrapRequest()
	invalid.Color = "red"
	requests = append(requests, invalid)
	for _, req := range requests {
		if _, err := NewWithClient(store, client).CreateRepositoryLabel(context.Background(), req); err == nil {
			t.Fatal("invalid/conflicting label accepted")
		}
	}
	if client.writes.Load() != 0 {
		t.Fatal("validation/existing name changed labels")
	}
	if _, err := NewWithClient(store, &fakeGitCodeClient{}).GetRepositoryMetadata(context.Background(), "fixture-a"); err == nil {
		t.Fatal("local binding substituted for provider metadata")
	}
}
