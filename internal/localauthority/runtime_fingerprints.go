package localauthority

import "gitcode-mcp/internal/authority"

// MutationIntentHash is pure protected request normalization shared with Store,
// not an authority lookup, receipt or consent to perform the mutation.
func MutationIntentHash(request authority.AttributedMutationRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	payload, err := canonicalPayload(request.Request)
	if err != nil {
		return "", err
	}
	request.Request.Payload = payload
	return mutationFingerprint(request.Request, request.ActorID), nil
}

func DocumentIntentHash(request authority.DocumentMutationCall) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	return documentFingerprint(request.Request, Actor{ID: request.ActorID})
}
