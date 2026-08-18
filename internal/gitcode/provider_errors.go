package gitcode

import (
	"errors"
	"fmt"
	"strings"
)

type ErrProviderUnavailable struct {
	Reason string
}

func (e ErrProviderUnavailable) Error() string {
	if e.Reason == "" {
		e.Reason = "provider unavailable"
	}
	return "gitcode: " + e.Reason
}

type ErrFixtureReadOnly struct {
	Operation string
}

func (e ErrFixtureReadOnly) Error() string {
	if e.Operation == "" {
		return "gitcode: fixture client is read-only"
	}
	return fmt.Sprintf("gitcode: fixture client is read-only for %s", e.Operation)
}

func (e ErrFixtureReadOnly) DiagnosticCode() string { return "fixture_read_only" }

func FixtureReadOnlyError(operation string) error {
	return ErrFixtureReadOnly{Operation: operation}
}

func IsFixtureReadOnly(err error) bool {
	var target ErrFixtureReadOnly
	return errors.As(err, &target)
}

type ErrUnsupportedCapability struct {
	CapabilityKey string
	Message       string
}

func (e ErrUnsupportedCapability) Error() string {
	if e.Message == "" {
		e.Message = "capability not supported"
	}
	return fmt.Sprintf("gitcode: unsupported capability %q: %s", e.CapabilityKey, e.Message)
}

func (e ErrUnsupportedCapability) DiagnosticCode() string { return "unsupported_capability" }

func IsUnsupportedCapability(err error) bool {
	var target ErrUnsupportedCapability
	return errors.As(err, &target)
}

type ErrValidationFailed struct {
	Field   string
	Message string
}

func (e ErrValidationFailed) Error() string {
	if e.Message == "" {
		e.Message = "validation failed"
	}
	if e.Field == "" {
		return "gitcode: " + e.Message
	}
	return fmt.Sprintf("gitcode: validation failed for %s: %s", e.Field, e.Message)
}

func (e ErrValidationFailed) DiagnosticCode() string { return "validation_failed" }

type ErrSchemaDecode struct {
	Endpoint string
	Field    string
	Expected string
	Received string
	Message  string
}

func (e ErrSchemaDecode) Error() string {
	msg := e.Message
	if msg == "" {
		msg = "schema decode failure"
	}
	if e.Field == "" {
		return "gitcode: " + msg
	}
	result := fmt.Sprintf("gitcode: schema decode failure for %s", e.Field)
	if e.Expected != "" && e.Received != "" {
		result += fmt.Sprintf(": expected %s, received %s", e.Expected, e.Received)
	}
	return result
}

func (e ErrSchemaDecode) DiagnosticCode() string { return "schema_decode" }

type ErrUnexpectedContentType struct {
	Endpoint     string
	ContentType  string
	ResponseSize int64
	Attempts     int
}

func (e ErrUnexpectedContentType) Error() string {
	contentType := strings.TrimSpace(e.ContentType)
	if contentType == "" {
		contentType = "missing"
	}
	return fmt.Sprintf("gitcode: unexpected content type for %s: %s", e.Endpoint, contentType)
}

func (e ErrUnexpectedContentType) DiagnosticCode() string { return "unexpected_content_type" }

type ErrMalformedJSON struct {
	Endpoint     string
	ContentType  string
	ResponseSize int64
	Offset       int64
	Attempts     int
}

func (e ErrMalformedJSON) Error() string {
	if e.Offset > 0 {
		return fmt.Sprintf("gitcode: malformed JSON for %s at byte %d", e.Endpoint, e.Offset)
	}
	return fmt.Sprintf("gitcode: malformed JSON for %s", e.Endpoint)
}

func (e ErrMalformedJSON) DiagnosticCode() string { return "malformed_response" }

type ErrPaginationMalformed struct {
	Endpoint string
	State    PageState
	Message  string
}

func (e ErrPaginationMalformed) Error() string {
	if e.Message == "" {
		e.Message = "malformed pagination state"
	}
	return fmt.Sprintf("gitcode: malformed pagination for %s: %s", e.Endpoint, e.Message)
}

type ErrPaginationLoop struct {
	Endpoint string
	State    PageState
}

func (e ErrPaginationLoop) Error() string {
	return fmt.Sprintf("gitcode: pagination loop for %s at page=%d cursor=%q", e.Endpoint, e.State.Page, e.State.Cursor)
}
