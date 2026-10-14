package localgit

import (
	"strings"
	"unicode/utf8"
)

// ValidateCitationResponse checks the closed public locator shape, not that a
// remote peer actually owns the Git object. Store reads provide that evidence.
func ValidateCitationResponse(c Citation) error {
	if !projectID.MatchString(c.ProjectID) || !oidValid(c.Commit) || !oidValid(c.Blob) {
		return fail("invalid_schema", "citation")
	}
	ref, _, ok := parseEntityPath(c.Path)
	if !ok || ref.Kind != c.Kind || ref.ID != c.ID {
		return fail("invalid_schema", "citation")
	}
	return nil
}

func ValidateEntityResponse(e Entity) error {
	if err := validateMetadata(e.Metadata); err != nil {
		return err
	}
	if err := ValidateCitationResponse(e.Citation); err != nil {
		return err
	}
	path, err := entityPath(e.Metadata.Kind, e.Metadata.ID, e.Metadata.ParentID)
	if err != nil || e.Citation.Kind != e.Metadata.Kind || e.Citation.ID != e.Metadata.ID || e.Citation.Path != path {
		return fail("invalid_schema", "citation")
	}
	if len(e.Body) > MaxFileBytes || !utf8.ValidString(e.Body) || strings.ContainsRune(e.Body, 0) {
		return fail("invalid_schema", "body")
	}
	return nil
}
