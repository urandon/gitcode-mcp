package localauthority

import (
	"context"
	"encoding/json"
	"time"

	"gitcode-mcp/internal/authority"
)

type runtimeProject struct {
	store *Store
	// Context-aware project serialization additionally fences future atomic
	// projection invalidation/publication with the canonical domain operation.
	lane chan struct{}
}

func (r *Runtime) capabilities() (authority.Negotiation, error) {
	if r == nil {
		return authority.Negotiation{}, authority.Error{Code: authority.Unavailable}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.serving || r.runContext == nil || r.runContext.Err() != nil {
		return authority.Negotiation{}, authority.Error{Code: authority.Unavailable}
	}
	return authority.Negotiation{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1, Available: true,
		Methods:           []string{"Read", "MutateAttributed", "MutateDocument", "KnowledgeStatus", "DocumentOperationStatus", "CleanupDocumentPin", "RevisionMetadata", "Audit", "MutationReceipt"},
		DocumentSchemaMin: 1, DocumentSchemaMax: 1}, nil
}

// Dispatch is a shared, protected prepared-runtime service operation. Product
// transports must use qualified IPC; this does not initialize or start a daemon.
// The current legacy RPC service still advertises ContractsOnly until its typed
// transport/client integration is completed. Search/provider gates remain open.
func (r *Runtime) Dispatch(ctx context.Context, method string, raw []byte) (any, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if r == nil || r.calls == nil {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	// Keep admission bounded even for trusted in-process service consumers,
	// rather than relying solely on the private transport's connection bound.
	select {
	case r.calls <- struct{}{}:
	default:
		return nil, Error{Capacity}
	}
	defer func() { <-r.calls }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := authority.DecodeRuntimeRequest(method, raw)
	if err != nil {
		return nil, err
	}
	if method == "Negotiate" {
		return r.capabilities()
	}
	caps, err := r.capabilities()
	if err != nil {
		return nil, err
	}
	if !caps.Supports(method) {
		return nil, authority.Error{Code: authority.Unsupported}
	}
	var project authority.UUID
	switch request := request.(type) {
	case authority.ReadRequest:
		project = request.ProjectID
	case authority.AttributedMutationRequest:
		project = request.Request.Intent.ProjectID
	case authority.DocumentMutationCall:
		project = request.Request.Intent.ProjectID
	case authority.ProjectRequest:
		project = request.ProjectID
	case authority.OriginalOperationRequest:
		project = request.ProjectID
	case authority.CleanupDocumentRequest:
		project = request.ProjectID
	case authority.Citation:
		project = request.ProjectID
	case authority.AuditRequest:
		project = request.ProjectID
	default:
		return nil, authority.Error{Code: authority.Unsupported}
	}
	r.mu.RLock()
	entry, serving := r.projects[project], r.serving
	runtimeContext := r.runContext
	if serving && entry != nil {
		r.operations.Add(1)
	}
	r.mu.RUnlock()
	if !serving {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	if entry == nil {
		return nil, authority.Error{Code: authority.NoAuthority}
	}
	defer r.operations.Done()
	stopped := make(chan struct{})
	stop := context.AfterFunc(runtimeContext, func() { cancel(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-entry.lane:
	}
	defer func() { entry.lane <- struct{}{} }()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	result, err := dispatchStore(ctx, entry.store, method, request)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(result)
	// Reserve room for the finite JSON-RPC envelope and numeric id. Transport
	// independently validates the actual encoded envelope before writing it.
	if err != nil || len(body) > authority.MaxContractBytes-256 {
		return nil, Error{Capacity}
	}
	return result, nil
}

func dispatchStore(ctx context.Context, store *Store, method string, request any) (any, error) {
	switch method {
	case "Read":
		r := request.(authority.ReadRequest)
		if r.Operation == "status" {
			return store.Status(ctx)
		}
		return store.Read(ctx, r)
	case "MutateAttributed":
		r := request.(authority.AttributedMutationRequest)
		return store.Mutate(ctx, r.Request, Actor{ID: r.ActorID})
	case "MutateDocument":
		r := request.(authority.DocumentMutationCall)
		return store.MutateDocument(ctx, r.Request, Actor{ID: r.ActorID})
	case "KnowledgeStatus":
		return store.KnowledgeStatus(ctx, request.(authority.ProjectRequest).ProjectID)
	case "DocumentOperationStatus", "CleanupDocumentPin":
		var original authority.OriginalOperationRequest
		var state DocumentOperationState
		var err error
		if method == "DocumentOperationStatus" {
			original = request.(authority.OriginalOperationRequest)
			state, err = store.DocumentOperationStatus(ctx, original.ProjectID, original.IdempotencyKey)
		} else {
			original = request.(authority.CleanupDocumentRequest).Original()
			state, err = store.CleanupDocumentPin(ctx, original.ProjectID, original.IdempotencyKey)
		}
		if err != nil {
			return nil, err
		}
		digest, err := original.KeyHash()
		if err != nil {
			return nil, err
		}
		result := DocumentOperationResult{Schema: 1, IdempotencyKeyHash: digest, State: state}
		return result, result.Match(original)
	case "RevisionMetadata":
		result, err := store.Metadata(ctx, request.(authority.Citation))
		if err != nil {
			return nil, err
		}
		return result, result.Validate()
	case "Audit":
		r := request.(authority.AuditRequest)
		entries, err := store.Audit(ctx, r.After, r.Limit)
		if err != nil {
			return nil, err
		}
		result := AuditResult{ProjectID: r.ProjectID, Entries: entries}
		return result, result.Match(r)
	case "MutationReceipt":
		r := request.(authority.OriginalOperationRequest)
		result, err := store.Receipt(ctx, r.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		digest, err := r.KeyHash()
		if err != nil || result.Validate() != nil || result.ProjectID != r.ProjectID || result.IdempotencyKeyHash != digest {
			return nil, Error{Corrupt}
		}
		return result, nil
	}
	return nil, authority.Error{Code: authority.Unsupported}
}
