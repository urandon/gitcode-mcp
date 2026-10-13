package localauthority

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestVerifyDocumentReceiptBindsExactLiveMarkdown(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	ctx := context.Background()
	request := documentRequest(t, "document.create", "synthetic-verify-create", authority.EmptyKnowledgeRef, authority.Citation{}, "# Synthetic Unicode content\n\nТекст\n")
	for _, operation := range []string{"document.create", "document.update"} {
		result, err := s.MutateDocument(ctx, request, testActor)
		if err != nil || VerifyDocumentReceipt(request, testActor, result) != nil {
			t.Fatal("genuine document confirmation rejected")
		}
		for name, tamper := range map[string]func(*authority.DocumentLocator){
			"digest": func(l *authority.DocumentLocator) { l.SHA256 = strings.Repeat("a", 64) },
			"length": func(l *authority.DocumentLocator) { l.ByteLength++ },
			"blob":   func(l *authority.DocumentLocator) { l.BlobOID = strings.Repeat("b", 64) },
		} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				changed := result
				locator := *result.Result.Locator
				changed.Result.Locator = &locator
				tamper(&locator)
				if changed.Validate() != nil {
					t.Fatal("tampered result is not a structurally valid counterexample")
				}
				var invalid authority.Error
				if !errors.As(VerifyDocumentReceipt(request, testActor, changed), &invalid) || invalid.Code != authority.InvalidContract {
					t.Fatal("valid receipt accepted contradictory document bytes")
				}
			})
		}
		replayed, err := s.MutateDocument(ctx, request, testActor)
		if err != nil || !replayed.Receipt.Replayed || VerifyDocumentReceipt(request, testActor, replayed) != nil {
			t.Fatal("genuine replay rejected")
		}
		request = documentRequest(t, "document.update", "synthetic-verify-update", result.Result.Locator.CommitOID, result.Receipt.Citation, "# Updated exact content\n")
	}
}
