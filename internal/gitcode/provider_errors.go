package gitcode

import "fmt"

type ErrProviderUnavailable struct {
	Reason string
}

func (e ErrProviderUnavailable) Error() string {
	if e.Reason == "" {
		e.Reason = "provider unavailable"
	}
	return "gitcode: " + e.Reason
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
