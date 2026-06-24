package gitcode

import (
	"errors"
	"fmt"
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
