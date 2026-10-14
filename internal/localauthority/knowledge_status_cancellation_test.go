package localauthority

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func TestKnowledgeStatusCancellationIsNotCorruption(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		for _, phase := range []string{"knowledge_layout_object", "knowledge_history_entry", "knowledge_status_head"} {
			name := phase + "/cancelled"
			if deadline {
				name = phase + "/deadline"
			}
			t.Run(name, func(t *testing.T) {
				s, _, _ := readyDocumentStore(t)
				req := documentRequest(t, "document.create", "synthetic-status-cancellation", authority.EmptyKnowledgeRef, authority.Citation{}, "# Immutable cancellation fixture\n")
				written, err := s.MutateDocument(context.Background(), req, testActor)
				if err != nil {
					t.Fatal("status cancellation canonical fixture unavailable")
				}
				before, err := s.Status(context.Background())
				if err != nil {
					t.Fatal("status cancellation tracker preimage unavailable")
				}
				baseline, err := s.KnowledgeStatus(context.Background(), testProject)
				if err != nil || baseline.Phase != "ready" {
					t.Fatal("status cancellation knowledge preimage unavailable")
				}
				ctx, cancel := context.WithCancel(context.Background())
				wantError := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
					wantError = context.DeadlineExceeded
				}
				defer cancel()
				hits, afterCancellation := 0, 0
				s.knowledgeReadCheckpoint = func(at string) {
					if ctx.Err() != nil {
						afterCancellation++
					}
					if at == phase {
						hits++
						if deadline {
							<-ctx.Done()
						} else {
							cancel()
						}
					}
				}
				s.fault = func(string) error { t.Fatal("status cancellation invoked a publication barrier"); return nil }
				_, err = s.KnowledgeStatus(ctx, testProject)
				s.knowledgeReadCheckpoint, s.fault = nil, nil
				if hits != 1 || afterCancellation != 0 || !errors.Is(err, wantError) {
					t.Fatal("interrupted actual Git status became corruption/readiness or kept traversing")
				}
				after, err := s.Status(context.Background())
				if err != nil || after != before || s.fenced || s.logicalFenced {
					t.Fatal("cancelled status changed tracker readiness or source generation")
				}
				verified, err := s.KnowledgeStatus(context.Background(), testProject)
				if err != nil || verified.Phase != "ready" || verified.CurrentRefOID != baseline.CurrentRefOID {
					t.Fatal("cancelled status changed canonical Git readiness/ref")
				}
				if replay, err := s.MutateDocument(context.Background(), req, testActor); err != nil || !replay.Receipt.Replayed || replay.Receipt.Citation != written.Receipt.Citation {
					t.Fatal("cancelled status changed original canonical receipt")
				}
			})
		}
	}
}
