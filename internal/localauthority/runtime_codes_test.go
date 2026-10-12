package localauthority

import (
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestStoreErrorsMatchFiniteRuntimeVocabulary(t *testing.T) {
	for _, code := range []Code{ReadOnly, Corrupt, DurabilityUnavailable, Capacity, StorageUnavailable, CommitUncertain,
		InitializationPending, RefConflict, KnowledgeMigrationRequired, KnowledgeSetupRequired,
		KnowledgeInitializationPending, DocumentPending, DocumentRecoveryRequired, KnowledgeCorrupt} {
		wire := authority.Code(code)
		if !authority.KnownCode(wire) || (authority.Error{Code: wire}).Error() != (Error{Code: code}).Error() {
			t.Fatal("store code or fixed diagnostic cannot cross the authority transport")
		}
	}
}
