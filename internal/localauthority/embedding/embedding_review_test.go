package embedding

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReviewOversizedQueryRejectedBeforeAmplification(t *testing.T) {
	p, err := Prepare(fixtureSpec("http://127.0.0.1:1"))
	if err != nil {
		t.Fatal("pure preparation unavailable")
	}
	defer p.Close(context.Background())
	input := strings.Repeat("x", 1<<20)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = p.Embed(context.Background(), Query, []string{input})
	runtime.ReadMemStats(&after)
	requireCode(t, err, InvalidInput)
	if after.TotalAlloc-before.TotalAlloc > 128<<10 {
		t.Fatalf("rejected oversized input allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
	}
}

func TestReviewProviderAdmissionAndCloseDoNotWaitPastDeadline(t *testing.T) {
	for _, method := range []string{"embed", "close"} {
		t.Run(method, func(t *testing.T) {
			p, err := Prepare(fixtureSpec("http://127.0.0.1:1"))
			if err != nil {
				t.Fatal("pure preparation unavailable")
			}
			p.mu.Lock()
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if method == "close" {
					done <- p.Close(ctx)
					return
				}
				_, e := p.Embed(ctx, Document, []string{"synthetic bounded input"})
				done <- e
			}()
			select {
			case <-done:
				p.mu.Unlock()
				p.Close(context.Background())
			case <-time.After(100 * time.Millisecond):
				p.mu.Unlock()
				<-done
				p.Close(context.Background())
				t.Fatal("provider operation waited beyond deadline under held admission mutex")
			}
		})
	}
}

func TestReviewValidOptionalEmbeddingMetricsMayBeAbsent(t *testing.T) {
	// Mirrors the upstream EmbedResponse omitempty contract, without importing
	// a provider library or contacting a server.
	body, err := json.Marshal(struct {
		Model           string      `json:"model"`
		Embeddings      [][]float32 `json:"embeddings"`
		TotalDuration   uint64      `json:"total_duration,omitempty"`
		LoadDuration    uint64      `json:"load_duration,omitempty"`
		PromptEvalCount uint64      `json:"prompt_eval_count,omitempty"`
	}{Model: fixtureModel, Embeddings: [][]float32{{3, 4}}})
	if err != nil {
		t.Fatal("closed upstream fixture unavailable")
	}
	vectors, err := decodeEmbedding(body, fixtureModel, 1, 2)
	if err != nil || len(vectors) != 1 || len(vectors[0]) != 2 {
		t.Fatal("valid omitted upstream timing metadata was rejected")
	}
}
