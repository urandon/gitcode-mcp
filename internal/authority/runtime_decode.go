package authority

// DecodeRuntimeRequest is the finite shared protected protocol boundary. It
// grants no admission; the runtime must implement and advertise a capability.
func DecodeRuntimeRequest(method string, raw []byte) (any, error) {
	decode := func(target interface{ Validate() error }) error { return DecodeJSON(raw, target) }
	switch method {
	case "Negotiate":
		var request NegotiationRequest
		err := decode(&request)
		return request, err
	case "Read":
		var request ReadRequest
		err := decode(&request)
		return request, err
	case "Mutate":
		var request MutationRequest
		err := decode(&request)
		return request, err
	case "MutateAttributed":
		var request AttributedMutationRequest
		err := decode(&request)
		return request, err
	case "MutateDocument":
		var request DocumentMutationCall
		err := decode(&request)
		return request, err
	case "KnowledgeStatus":
		var request ProjectRequest
		err := decode(&request)
		return request, err
	case "DocumentOperationStatus", "MutationReceipt":
		var request OriginalOperationRequest
		err := decode(&request)
		return request, err
	case "CleanupDocumentPin":
		var request CleanupDocumentRequest
		err := decode(&request)
		return request, err
	case "RevisionMetadata":
		var request Citation
		err := decode(&request)
		return request, err
	case "Audit":
		var request AuditRequest
		err := decode(&request)
		return request, err
	default:
		return nil, Error{Unsupported}
	}
}
