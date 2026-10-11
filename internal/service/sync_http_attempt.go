package service

import (
	"context"
	"errors"

	"gitcode-mcp/internal/diagnostics"
)

func beginSyncHTTPAttempt(ctx context.Context) (context.Context, func(error) error) {
	ctx, witness := diagnostics.WithHTTPAttemptWitness(ctx)
	return ctx, func(err error) error {
		if err == nil {
			return nil
		}
		if partial, ok := err.(*PartialSyncError); ok {
			partial.httpAttempted, partial.httpAttemptKnown = witness.Attempted(), true
			return partial
		}
		return diagnostics.RetainHTTPAttempt(err, witness.Attempted())
	}
}

func (e PartialSyncError) HTTPAttemptEvidence() (bool, bool) {
	return e.httpAttempted, e.httpAttemptKnown
}

func (e PartialSyncError) OperationInterruption() string {
	switch e.Diagnostic {
	case SyncDiagnosticCancelled:
		return "cancelled"
	case SyncDiagnosticTimeout:
		return "timeout"
	}
	return ""
}

// PreserveHTTPAttemptEvidence keeps receipts and the first interruption when
// transport surfaces combine independent collection errors.
func (e *PartialSyncError) PreserveHTTPAttemptEvidence(errs ...error) {
	e.httpAttempted, e.httpAttemptKnown = diagnostics.HTTPAttemptEvidence(errors.Join(errs...))
	for _, err := range errs {
		var partial *PartialSyncError
		if e.Diagnostic == "" && errors.As(err, &partial) {
			e.Diagnostic = partial.Diagnostic
		}
		if e.Diagnostic == "" {
			switch {
			case errors.Is(err, context.Canceled):
				e.Diagnostic = SyncDiagnosticCancelled
			case errors.Is(err, context.DeadlineExceeded):
				e.Diagnostic = SyncDiagnosticTimeout
			}
		}
	}
}
