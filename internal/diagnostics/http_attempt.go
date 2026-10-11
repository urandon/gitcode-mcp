package diagnostics

import (
	"context"
	"sync/atomic"
)

type httpAttemptKey struct{}

// HTTPAttemptWitness is one bounded operation-local bit, independent of
// telemetry. Nested service calls share the enclosing operation's witness.
// It retains no request, URL, identity, payload, or error text.
type HTTPAttemptWitness struct{ attempted atomic.Bool }

func WithHTTPAttemptWitness(ctx context.Context) (context.Context, *HTTPAttemptWitness) {
	if witness, ok := ctx.Value(httpAttemptKey{}).(*HTTPAttemptWitness); ok {
		return ctx, witness
	}
	witness := &HTTPAttemptWitness{}
	return context.WithValue(ctx, httpAttemptKey{}, witness), witness
}

func RecordHTTPAttempt(ctx context.Context) {
	if witness, ok := ctx.Value(httpAttemptKey{}).(*HTTPAttemptWitness); ok {
		witness.attempted.Store(true)
	}
}

func (w *HTTPAttemptWitness) Attempted() bool { return w != nil && w.attempted.Load() }

type httpAttemptError struct {
	cause     error
	attempted bool
}

func (e httpAttemptError) Error() string                     { return e.cause.Error() }
func (e httpAttemptError) Unwrap() error                     { return e.cause }
func (e httpAttemptError) HTTPAttemptEvidence() (bool, bool) { return e.attempted, true }

func (e httpAttemptError) DiagnosticCode() string {
	if codes := diagnosticCodes(e.cause); len(codes) != 0 {
		return codes[0]
	}
	return ""
}

// RetainHTTPAttempt freezes the bit at return, preserving the underlying error.
func RetainHTTPAttempt(err error, attempted bool) error {
	if err == nil {
		return nil
	}
	return httpAttemptError{cause: err, attempted: attempted}
}

// HTTPAttemptEvidence returns known evidence, not an inference from error text.
// An aggregate is definitely attempted if any child is; definitely not only
// when every child has known negative evidence. Outer operation receipts win.
func HTTPAttemptEvidence(err error) (attempted, known bool) {
	if err == nil {
		return false, false
	}
	if evidence, ok := err.(interface{ HTTPAttemptEvidence() (bool, bool) }); ok {
		if attempted, known = evidence.HTTPAttemptEvidence(); known {
			return attempted, true
		}
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		children := e.Unwrap()
		allKnown := len(children) != 0
		for _, child := range children {
			attempted, known := HTTPAttemptEvidence(child)
			if attempted && known {
				return true, true
			}
			allKnown = allKnown && known
		}
		return false, allKnown
	case interface{ Unwrap() error }:
		return HTTPAttemptEvidence(e.Unwrap())
	}
	return false, false
}
