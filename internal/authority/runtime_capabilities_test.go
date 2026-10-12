package authority

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeNegotiationPreservesLegacyUnavailable(t *testing.T) {
	caps := ContractsOnly()
	if caps.Validate() != nil || caps.Supports("Read") || caps.Supports("MutateDocument") {
		t.Fatal("unavailable runtime admitted a method")
	}
	raw, _ := json.Marshal(caps)
	if strings.Contains(string(raw), "document_schema") {
		t.Fatal("legacy unavailable response changed shape")
	}
	var decoded Negotiation
	if DecodeJSON(raw, &decoded) != nil || decoded.Available {
		t.Fatal("legacy negotiation no longer decodes")
	}
}

func TestRuntimeNegotiationFiniteMethodsAndSeparateDocumentSchema(t *testing.T) {
	base := Negotiation{Protocol: Protocol, Authority: Local, SchemaMin: 1, SchemaMax: 1, Available: true, Methods: []string{"Read", "Mutate"}}
	if base.Validate() != nil || !base.Supports("Read") || base.Supports("MutateDocument") {
		t.Fatal("tracker capability boundary failed")
	}
	docs := base
	docs.Methods = []string{"Read", "MutateDocument", "KnowledgeStatus", "DocumentOperationStatus", "CleanupDocumentPin"}
	docs.DocumentSchemaMin, docs.DocumentSchemaMax = 1, 1
	if docs.Validate() != nil || !docs.Supports("MutateDocument") || docs.Supports("Mutate") {
		t.Fatal("document capability was not separate")
	}
	for name, mutate := range map[string]func(*Negotiation){
		"unknown method":     func(n *Negotiation) { n.Methods = []string{"synthetic-private"} },
		"duplicate":          func(n *Negotiation) { n.Methods = []string{"Read", "Read"} },
		"case alias":         func(n *Negotiation) { n.Methods = []string{"read"} },
		"setup over IPC":     func(n *Negotiation) { n.Methods = []string{"SetupKnowledge"} },
		"empty":              func(n *Negotiation) { n.Methods = []string{} },
		"nil":                func(n *Negotiation) { n.Methods = nil },
		"doc without schema": func(n *Negotiation) { n.Methods = []string{"MutateDocument"} },
		"extra schema":       func(n *Negotiation) { n.DocumentSchemaMin, n.DocumentSchemaMax = 1, 1 },
		"other schema":       func(n *Negotiation) { n.SchemaMax = 2 },
		"reason":             func(n *Negotiation) { n.Reason = Unavailable },
		"handoff":            func(n *Negotiation) { n.Handoff = "synthetic-private" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if candidate.Validate() == nil || candidate.Supports("Read") {
				t.Fatal("malformed capability admitted work")
			}
			raw, _ := json.Marshal(candidate)
			if DecodeJSON(raw, &Negotiation{}) == nil {
				t.Fatal("malformed peer capability decoded")
			}
		})
	}
	for _, pair := range [][2]int{{0, 1}, {1, 0}, {1, 2}, {2, 2}} {
		candidate := docs
		candidate.DocumentSchemaMin, candidate.DocumentSchemaMax = pair[0], pair[1]
		if candidate.Validate() == nil {
			t.Fatal("incompatible document schema accepted")
		}
	}
}

func TestRuntimeKnownCodesRemainFiniteAndContentFree(t *testing.T) {
	codes := []Code{InvalidContract, Unavailable, Unsupported, NoAuthority, PolicyDenied, Conflict, RevisionConflict, InitializationRequired,
		ReadOnly, Corrupt, DurabilityUnavailable, Capacity, StorageUnavailable, CommitUncertain, InitializationPending, RefConflict,
		KnowledgeMigrationRequired, KnowledgeSetupRequired, KnowledgeInitializationPending, DocumentPending, DocumentRecoveryRequired, KnowledgeCorrupt}
	seen := map[Code]bool{}
	for _, code := range codes {
		if seen[code] || !KnownCode(code) || (Error{code}).Error() == (Error{InvalidContract}).Error() && code != InvalidContract {
			t.Fatal("known code missing fixed diagnostic identity")
		}
		seen[code] = true
	}
	for _, code := range []Code{"", "synthetic-private", "AUTHORITY_CORRUPT", "authority_corrupt\n"} {
		if KnownCode(code) || strings.Contains((Error{code}).Error(), "synthetic-private") {
			t.Fatal("unknown code was admitted or reflected")
		}
	}
}
