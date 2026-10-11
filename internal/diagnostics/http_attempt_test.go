package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestHTTPAttemptWitnessIsBoundedSharedAndFrozen(t *testing.T) {
	ctx, witness := WithHTTPAttemptWitness(context.Background())
	nested, shared := WithHTTPAttemptWitness(ctx)
	if witness != shared || witness.Attempted() {
		t.Fatal("nested witness must share one initially false bit")
	}
	before := RetainHTTPAttempt(context.Canceled, witness.Attempted())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); RecordHTTPAttempt(nested) }()
	}
	wg.Wait()
	if !witness.Attempted() {
		t.Fatal("transport entry not retained")
	}
	if attempted, known := HTTPAttemptEvidence(before); !known || attempted || !errors.Is(before, context.Canceled) {
		t.Fatal("receipt changed after return or lost cause")
	}
	positive := RetainHTTPAttempt(context.DeadlineExceeded, true)
	for _, err := range []error{positive, fmt.Errorf("outer: %w", positive), errors.Join(before, positive), errors.Join(errors.New("unknown"), positive)} {
		if attempted, known := HTTPAttemptEvidence(err); !known || !attempted {
			t.Fatal("positive aggregate evidence lost")
		}
	}
	if _, known := HTTPAttemptEvidence(errors.Join(before, errors.New("unknown"))); known {
		t.Fatal("unknown child is not negative evidence")
	}
	if attempted, known := HTTPAttemptEvidence(errors.Join(before, before)); !known || attempted {
		t.Fatal("known negative aggregate lost")
	}
}

func TestHTTPAttemptEvidenceOverridesInference(t *testing.T) {
	for _, tc := range []struct {
		cause     error
		attempted bool
		code      Code
	}{
		{context.Canceled, false, CodeOperationCancelled},
		{context.DeadlineExceeded, false, CodeOperationTimeout},
		{context.Canceled, true, CodeLiveTransportFailure},
		{context.DeadlineExceeded, true, CodeLiveTransportFailure},
	} {
		d := Classify(RetainHTTPAttempt(tc.cause, tc.attempted), CommandContext{ProviderMode: "live-http", HTTPAttempted: !tc.attempted, TransportFailure: true})
		if d.Code != tc.code || d.HTTPAttempted != tc.attempted {
			t.Fatal("classification contradicts transport evidence")
		}
	}
	// Configuration failures remain configuration failures even with a
	// cancellation cause; startup never becomes a network incident.
	d := Classify(RetainHTTPAttempt(context.Canceled, false), CommandContext{ProviderMode: "live-http", BroaderConfigurationInvalid: true})
	if d.Code != CodeConfigCredential || d.HTTPAttempted {
		t.Fatal("configuration precedence changed")
	}
	for _, ctx := range []CommandContext{
		{ProviderMode: "live-http", SchemaDecodeFailure: true, HTTPStatus: 200},
		{ProviderMode: "live-http", APIFailure: true, HTTPStatus: 400},
	} {
		ctx.HTTPAttempted = true
		want := Classify(errors.New("fixture provider failure"), ctx).Code
		d := Classify(RetainHTTPAttempt(context.Canceled, true), ctx)
		if d.Code != want || !d.HTTPAttempted {
			t.Fatal("late interruption masked prior provider/schema failure evidence")
		}
	}
}
