package gitcode

import (
	"fmt"
	"time"
)

type ErrNetworkUnavailable struct {
	Endpoint string
	Status   int
	Attempts int
	Cause    error
	Recovery string
}

func (e ErrNetworkUnavailable) Error() string {
	if e.Recovery == "" {
		e.Recovery = "retry with a longer timeout or check connectivity"
	}
	return fmt.Sprintf("gitcode: network unavailable for %s after %d attempt(s): %s", e.Endpoint, e.Attempts, e.Recovery)
}

func (e ErrNetworkUnavailable) Unwrap() error { return e.Cause }

func (e ErrNetworkUnavailable) DiagnosticCode() string { return "network_unavailable" }

type ErrRateLimited struct {
	RetryAfter    time.Duration
	RawRetryAfter string
	Endpoint      string
	Attempts      int
}

func (e ErrRateLimited) Error() string {
	return fmt.Sprintf("gitcode: rate limited for %s after %d attempt(s): retry after %s", e.Endpoint, e.Attempts, e.RetryAfter)
}

type ErrAuthExpired struct {
	Endpoint string
	Status   int
	Message  string
}

func (e ErrAuthExpired) Error() string {
	if e.Message == "" {
		e.Message = "authentication expired; renew your token and try again"
	}
	return fmt.Sprintf("gitcode: auth expired for %s: %s", e.Endpoint, e.Message)
}

func (e ErrAuthExpired) DiagnosticCode() string { return "auth_expired" }

type ErrForbidden struct {
	Endpoint string
	Status   int
	Message  string
	Recovery string
}

func (e ErrForbidden) Error() string {
	if e.Recovery == "" {
		e.Recovery = "check GitCode permissions for this resource"
	}
	if e.Message == "" {
		e.Message = "forbidden"
	}
	return fmt.Sprintf("gitcode: forbidden for %s: %s; %s", e.Endpoint, e.Message, e.Recovery)
}

func (e ErrForbidden) DiagnosticCode() string { return "forbidden" }

type ErrNotFound struct {
	Endpoint string
	ID       string
	Message  string
}

func (e ErrNotFound) Error() string {
	if e.Message == "" {
		e.Message = "not found"
	}
	return fmt.Sprintf("gitcode: %s at %s", e.Message, e.Endpoint)
}

type ErrConflict struct {
	Endpoint      string
	Status        int
	LocalPayload  []byte
	RemotePayload []byte
	Message       string
}

func (e ErrConflict) Error() string {
	if e.Message == "" {
		e.Message = "remote conflict"
	}
	return fmt.Sprintf("gitcode: conflict for %s: %s", e.Endpoint, e.Message)
}

type ErrPartialResponse struct {
	Endpoint string
	Expected int64
	Got      int64
	Cause    error
	Message  string
}

func (e ErrPartialResponse) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("gitcode: partial response for %s: %s", e.Endpoint, e.Message)
	}
	if e.Expected > 0 || e.Got > 0 {
		return fmt.Sprintf("gitcode: partial response for %s: expected %d bytes, got %d bytes", e.Endpoint, e.Expected, e.Got)
	}
	return fmt.Sprintf("gitcode: partial response for %s", e.Endpoint)
}

func (e ErrPartialResponse) Unwrap() error { return e.Cause }

type ErrRemoteCollision struct {
	Alias      string
	ExistingID string
	NewID      string
	Endpoint   string
}

func (e ErrRemoteCollision) Error() string {
	return fmt.Sprintf("gitcode: remote id %s already maps to %s; cannot map to %s", e.Alias, e.ExistingID, e.NewID)
}

type ErrRemoteNotFound struct {
	Endpoint string
	Alias    string
	Message  string
}

func (e ErrRemoteNotFound) Error() string {
	if e.Message == "" {
		e.Message = "remote record not found"
	}
	return fmt.Sprintf("gitcode: %s for alias %s at %s", e.Message, e.Alias, e.Endpoint)
}

type ErrAPIValidation struct {
	Endpoint string
	Status   int
	Message  string
}

func (e ErrAPIValidation) Error() string {
	if e.Message == "" {
		e.Message = "api validation failed"
	}
	return fmt.Sprintf("gitcode: api validation failed for %s: %s", e.Endpoint, e.Message)
}

func (e ErrAPIValidation) DiagnosticCode() string { return "api_validation" }

type ErrPayloadTooLarge struct {
	Endpoint string
	Limit    int64
	Size     int64
	Source   string
}

func (e ErrPayloadTooLarge) Error() string {
	return fmt.Sprintf("gitcode: response for %s exceeds maximum size %d bytes", e.Endpoint, e.Limit)
}

func (e ErrPayloadTooLarge) FailureSource() string { return e.Source }
