package authority

// Runtime error vocabulary is shared by authority stores and transports. An
// error code is not evidence of admission; unknown peer codes remain refused.
const (
	ReadOnly                       Code = "authority_read_only"
	Corrupt                        Code = "authority_corrupt"
	DurabilityUnavailable          Code = "authority_durability_unavailable"
	Capacity                       Code = "authority_capacity"
	StorageUnavailable             Code = "authority_storage_unavailable"
	CommitUncertain                Code = "authority_commit_uncertain"
	InitializationPending          Code = "authority_initialization_pending"
	RefConflict                    Code = "authority_ref_conflict"
	KnowledgeMigrationRequired     Code = "authority_knowledge_migration_required"
	KnowledgeSetupRequired         Code = "authority_knowledge_setup_required"
	KnowledgeInitializationPending Code = "authority_knowledge_initialization_pending"
	DocumentPending                Code = "authority_document_pending"
	DocumentRecoveryRequired       Code = "authority_document_recovery_required"
	KnowledgeCorrupt               Code = "authority_knowledge_corrupt"
)

func KnownCode(code Code) bool {
	switch code {
	case InvalidContract, Unavailable, Unsupported, NoAuthority, PolicyDenied,
		Conflict, RevisionConflict, InitializationRequired, ReadOnly, Corrupt,
		DurabilityUnavailable, Capacity, StorageUnavailable, CommitUncertain,
		InitializationPending, RefConflict, KnowledgeMigrationRequired,
		KnowledgeSetupRequired, KnowledgeInitializationPending, DocumentPending,
		DocumentRecoveryRequired, KnowledgeCorrupt:
		return true
	}
	return false
}

func knownRuntimeMethod(method string) bool {
	switch method {
	case "Read", "Mutate", "MutateAttributed", "MutateDocument", "KnowledgeStatus",
		"DocumentOperationStatus", "CleanupDocumentPin", "Search",
		"ProjectionStatus", "ReconcileProjection", "CancelProjection",
		"RevisionMetadata", "Audit", "MutationReceipt":
		return true
	}
	return false
}

func documentRuntimeMethod(method string) bool {
	switch method {
	case "MutateDocument", "KnowledgeStatus", "DocumentOperationStatus", "CleanupDocumentPin":
		return true
	}
	return false
}

// Validate permits only implemented, finite contracts. The legacy unavailable
// response retains its exact shape and semantics; added optional capability
// fields never grant generic Mutate permission to dispatch document effects.
func (n Negotiation) Validate() error {
	if n.Protocol != Protocol || n.Authority != Local || n.SchemaMin != Schema || n.SchemaMax != Schema || n.Methods == nil {
		return Error{Unsupported}
	}
	if !n.Available {
		if len(n.Methods) != 0 || n.Reason != Unavailable || n.Handoff != ContractsOnly().Handoff || n.DocumentSchemaMin != 0 || n.DocumentSchemaMax != 0 {
			return Error{Unsupported}
		}
		return nil
	}
	if len(n.Methods) == 0 || len(n.Methods) > 14 || n.Reason != "" || n.Handoff != "" {
		return Error{Unsupported}
	}
	seen := make(map[string]bool, len(n.Methods))
	documents := false
	for _, method := range n.Methods {
		if !knownRuntimeMethod(method) || seen[method] {
			return Error{Unsupported}
		}
		seen[method] = true
		documents = documents || documentRuntimeMethod(method)
	}
	if documents {
		if n.DocumentSchemaMin != DocumentContractSchema || n.DocumentSchemaMax != DocumentContractSchema {
			return Error{Unsupported}
		}
	} else if n.DocumentSchemaMin != 0 || n.DocumentSchemaMax != 0 {
		return Error{Unsupported}
	}
	return nil
}

func (n Negotiation) Supports(method string) bool {
	if n.Validate() != nil || !n.Available {
		return false
	}
	for _, implemented := range n.Methods {
		if implemented == method {
			return true
		}
	}
	return false
}
